// Package channelmgr manages the lifecycle of communication channel instances (Telegram bots, etc.).
// It supports runtime add/start/stop/restart of channels and multiplexes MessageSender
// and StreamingSender calls to the correct running channel instance.
package channelmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	consolech "github.com/iulita-ai/iulita/internal/channel/console"
	discordch "github.com/iulita-ai/iulita/internal/channel/discord"
	"github.com/iulita-ai/iulita/internal/channel/telegram"
	"github.com/iulita-ai/iulita/internal/channel/webchat"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/ratelimit"
	"github.com/iulita-ai/iulita/internal/skill/interact"
	"github.com/iulita-ai/iulita/internal/storage"
)

// DiscordInstanceConfig is the JSON stored in channel_instances.config for Discord channels.
type DiscordInstanceConfig struct {
	Token             string   `json:"token"`
	AllowedChannelIDs []string `json:"allowed_channel_ids"`
	RateLimit         int      `json:"rate_limit"`
	RateWindow        string   `json:"rate_window"`
}

// TelegramInstanceConfig is the JSON stored in channel_instances.config for Telegram channels
// created via the dashboard. Config-sourced channels use config.toml values directly.
type TelegramInstanceConfig struct {
	Token          string  `json:"token"`
	AllowedIDs     []int64 `json:"allowed_ids"`
	DebounceWindow string  `json:"debounce_window"`
	RateLimit      int     `json:"rate_limit"`
	RateWindow     string  `json:"rate_window"`
}

// CommandRegistrar is called once for each new Telegram channel instance to register slash commands.
type CommandRegistrar func(tg *telegram.Channel)

// ManagedChannel holds the runtime state for a single running channel instance.
type ManagedChannel struct {
	Instance domain.ChannelInstance
	tg       *telegram.Channel  // non-nil for Telegram instances
	web      *webchat.Channel   // non-nil for Web Chat instances
	discord  *discordch.Channel // non-nil for Discord instances
	console  *consolech.Channel // non-nil for Console instances
	cancel   context.CancelFunc
	done     chan struct{} // closed when Start() returns
}

// Config holds all dependencies and settings for creating a Manager.
type Config struct {
	Store        storage.Repository
	CfgStore     *config.Store // may be nil; used to decrypt dashboard channel configs
	HTTPClient   *http.Client  // may be nil; used for Telegram API calls
	UserResolver channel.UserResolver

	// ClearFn is called by the /clear command to wipe chat history.
	ClearFn func(ctx context.Context, chatID string) error

	// Config-sourced Telegram settings (from config.toml).
	ConfigToken      string
	ConfigAllowedIDs []int64
	ConfigDebounce   time.Duration
	ConfigRateLimit  int
	ConfigRateWindow time.Duration

	// Transcriber for voice messages (nil = disabled).
	Transcriber telegram.TranscriptionProvider

	Logger *zap.Logger
}

// Manager manages the lifecycle of all communication channel instances.
// It implements channel.MessageSender and channel.StreamingSender as a multiplexer,
// routing calls to the correct running channel based on chatID lookup.
type Manager struct {
	mu      sync.RWMutex
	running map[string]*ManagedChannel // instanceID → running channel

	store        storage.Repository
	cfgStore     *config.Store
	httpClient   *http.Client
	userResolver channel.UserResolver
	clearFn      func(ctx context.Context, chatID string) error

	// Config-sourced Telegram settings.
	configTokenMu    sync.RWMutex // protects configToken independently from mu
	configToken      string
	configAllowedIDs []int64
	configDebounce   time.Duration
	configRateLimit  int
	configRateWindow time.Duration

	transcriber telegram.TranscriptionProvider

	commandRegistrar  CommandRegistrar       // set via SetCommandRegistrar
	handler           channel.MessageHandler // set by StartAll
	startCtx          context.Context        // captured from StartAll for hot-reload restarts
	consoleUserID     string                 // pre-resolved user ID for console channel
	consoleStatusProv *consolech.StatusProvider
	consoleCompactFn  consolech.CompactFunc
	consoleOnExit     func() // called when console TUI exits

	logger *zap.Logger
	wg     sync.WaitGroup // tracks all channel goroutines
}

// New creates a new Manager with the given configuration.
func New(cfg Config) *Manager {
	return &Manager{
		running:          make(map[string]*ManagedChannel),
		store:            cfg.Store,
		cfgStore:         cfg.CfgStore,
		httpClient:       cfg.HTTPClient,
		userResolver:     cfg.UserResolver,
		clearFn:          cfg.ClearFn,
		configToken:      cfg.ConfigToken,
		configAllowedIDs: cfg.ConfigAllowedIDs,
		configDebounce:   cfg.ConfigDebounce,
		configRateLimit:  cfg.ConfigRateLimit,
		configRateWindow: cfg.ConfigRateWindow,
		transcriber:      cfg.Transcriber,
		logger:           cfg.Logger,
	}
}

// SetCommandRegistrar sets the callback used to register slash commands on each new channel.
// Must be called before StartAll.
func (m *Manager) SetCommandRegistrar(fn CommandRegistrar) {
	m.commandRegistrar = fn
}

// SetConsoleUserID sets the pre-resolved user ID for console channel instances.
func (m *Manager) SetConsoleUserID(userID string) {
	m.consoleUserID = userID
}

// SetConsoleStatusProvider sets the status provider for console /status command.
func (m *Manager) SetConsoleStatusProvider(sp *consolech.StatusProvider) {
	m.consoleStatusProv = sp
}

// SetConsoleCompactFunc sets the compact function for console /compact command.
func (m *Manager) SetConsoleCompactFunc(fn consolech.CompactFunc) {
	m.consoleCompactFn = fn
}

// SetConsoleOnExit sets a callback invoked when the console TUI exits.
// Typically used to trigger application shutdown.
func (m *Manager) SetConsoleOnExit(fn func()) {
	m.consoleOnExit = fn
}

// UpdateConfigToken atomically updates the token used for config-sourced Telegram instances
// and restarts all running config-sourced Telegram channels with the new token.
// If token is empty, running config-sourced Telegram channels are stopped.
func (m *Manager) UpdateConfigToken(token string) {
	m.configTokenMu.Lock()
	m.configToken = token
	ctx := m.startCtx
	m.configTokenMu.Unlock()

	if ctx == nil || m.handler == nil {
		return // not yet started; StartAll will pick up configToken
	}
	if ctx.Err() != nil {
		return // shutting down, don't restart channels
	}

	// Find running config-sourced Telegram instances.
	m.mu.RLock()
	var toRestart []domain.ChannelInstance
	for _, mc := range m.running {
		if mc.Instance.Source == domain.ChannelSourceConfig &&
			mc.Instance.Type == domain.ChannelTypeTelegram {
			toRestart = append(toRestart, mc.Instance)
		}
	}
	m.mu.RUnlock()

	for _, inst := range toRestart {
		if token == "" {
			m.StopInstance(inst.ID)
			m.logger.Info("config-sourced telegram stopped (token removed)",
				zap.String("id", inst.ID))
		} else {
			if err := m.restartInstance(ctx, inst); err != nil {
				m.logger.Error("failed to restart telegram after token change",
					zap.String("id", inst.ID), zap.Error(err))
			} else {
				m.logger.Info("config-sourced telegram restarted with new token",
					zap.String("id", inst.ID))
			}
		}
	}

	// If token is set but no config-sourced instance was running, try to start one from DB.
	if token != "" && len(toRestart) == 0 {
		m.startConfigTelegramFromDB(ctx)
	}
}

// startConfigTelegramFromDB looks up the config-sourced Telegram instance in the DB and starts it.
// Used when a token is set at runtime but no instance was running.
func (m *Manager) startConfigTelegramFromDB(ctx context.Context) {
	instances, err := m.store.ListChannelInstances(ctx)
	if err != nil {
		m.logger.Error("failed to list channel instances for telegram hot-start", zap.Error(err))
		return
	}
	for _, inst := range instances {
		if inst.Source == domain.ChannelSourceConfig &&
			inst.Type == domain.ChannelTypeTelegram &&
			inst.Enabled {
			if err := m.startInstance(ctx, inst); err != nil {
				m.logger.Error("failed to start config telegram on token set",
					zap.String("id", inst.ID), zap.Error(err))
			} else {
				m.logger.Info("config-sourced telegram started via hot-reload",
					zap.String("id", inst.ID))
			}
			return
		}
	}
	m.logger.Debug("no config-sourced telegram instance found in DB to hot-start")
}

// GetConsole returns the console.Channel if a console instance is running.
func (m *Manager) GetConsole() *consolech.Channel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, mc := range m.running {
		if mc.console != nil {
			return mc.console
		}
	}
	return nil
}

// StartAll loads all enabled channel instances from the DB and starts each in a goroutine.
// Returns immediately; use StopAll to wait for all channels to finish.
func (m *Manager) StartAll(ctx context.Context, handler channel.MessageHandler) error {
	m.handler = handler
	m.configTokenMu.Lock()
	m.startCtx = ctx
	m.configTokenMu.Unlock()

	instances, err := m.store.ListChannelInstances(ctx)
	if err != nil {
		return fmt.Errorf("loading channel instances: %w", err)
	}

	started := 0
	for _, inst := range instances {
		if !inst.Enabled {
			continue
		}
		if !m.isSupportedType(inst.Type) {
			m.logger.Debug("skipping unsupported channel type",
				zap.String("id", inst.ID), zap.String("type", inst.Type))
			continue
		}
		if err := m.startInstance(ctx, inst); err != nil {
			m.logger.Error("failed to start channel instance",
				zap.String("id", inst.ID), zap.Error(err))
			// Continue starting other instances rather than aborting.
		} else {
			started++
		}
	}

	m.logger.Info("channel manager started", zap.Int("channels", started))
	return nil
}

// StopAll stops all running channel instances and waits for them to finish.
func (m *Manager) StopAll() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	for _, id := range ids {
		m.StopInstance(id)
	}
	m.wg.Wait()
}

// AddInstance starts a newly created channel instance at runtime.
// Should be called after the instance has been saved to the DB.
func (m *Manager) AddInstance(ctx context.Context, instance domain.ChannelInstance) error {
	if !instance.Enabled {
		return nil
	}
	if !m.isSupportedType(instance.Type) {
		return nil
	}
	if m.handler == nil {
		return fmt.Errorf("channel manager not started; call StartAll first")
	}
	return m.startInstance(ctx, instance)
}

// UpdateInstance handles a change to an existing channel instance.
// If enabled: restart (applies new config). If disabled: stop.
// If not yet running and now enabled: start.
func (m *Manager) UpdateInstance(ctx context.Context, instance domain.ChannelInstance) error {
	if !m.isSupportedType(instance.Type) {
		return nil
	}

	m.mu.RLock()
	_, isRunning := m.running[instance.ID]
	m.mu.RUnlock()

	if !instance.Enabled {
		if isRunning {
			m.StopInstance(instance.ID)
		}
		return nil
	}

	if isRunning {
		return m.restartInstance(ctx, instance)
	}
	if m.handler == nil {
		return nil // manager not yet started; will be picked up on next StartAll
	}
	return m.startInstance(ctx, instance)
}

// StopInstance stops a specific channel instance by ID and waits for it to finish.
// It gives the instance up to 5 seconds to stop gracefully before giving up.
func (m *Manager) StopInstance(instanceID string) {
	m.mu.Lock()
	mc, ok := m.running[instanceID]
	if ok {
		delete(m.running, instanceID)
	}
	m.mu.Unlock()

	if !ok {
		return
	}

	m.logger.Info("stopping channel instance", zap.String("id", instanceID))
	mc.cancel()

	select {
	case <-mc.done:
		m.logger.Info("channel instance stopped", zap.String("id", instanceID))
	case <-time.After(5 * time.Second):
		m.logger.Warn("channel instance did not stop in time, proceeding",
			zap.String("id", instanceID))
	}
}

// IsRunning returns true if the given channel instance is currently active.
func (m *Manager) IsRunning(instanceID string) bool {
	m.mu.RLock()
	_, ok := m.running[instanceID]
	m.mu.RUnlock()
	return ok
}

// RunningIDs returns the IDs of all currently running channel instances.
func (m *Manager) RunningIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	return ids
}

// SetBotPhoto sets the profile photo for the Telegram channel identified by instanceID.
func (m *Manager) SetBotPhoto(ctx context.Context, instanceID string, data []byte) error {
	m.mu.RLock()
	mc, ok := m.running[instanceID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("channel instance %q is not running", instanceID)
	}
	if mc.tg == nil {
		return fmt.Errorf("channel instance %q is not a Telegram channel", instanceID)
	}
	return mc.tg.SetBotPhoto(ctx, data)
}

// --- SendMessage / StartStream (channel.MessageSender + channel.StreamingSender) ---

// SendMessage routes the message to the channel instance associated with chatID.
// Falls back to the first running channel if no specific instance is found.
func (m *Manager) SendMessage(ctx context.Context, chatID string, text string) error {
	sender := m.senderFor(ctx, chatID)
	if sender == nil {
		return fmt.Errorf("no running channel available to send message to chat %s", chatID)
	}
	return sender.SendMessage(ctx, chatID, text)
}

// StartStream opens a streaming session on the channel instance associated with chatID.
// Falls back to the first running channel if no specific instance is found.
func (m *Manager) StartStream(ctx context.Context, chatID string, replyTo int) (func(string), func(string), error) {
	sender := m.senderFor(ctx, chatID)
	if sender == nil {
		return nil, nil, fmt.Errorf("no running channel available for streaming to chat %s", chatID)
	}
	return sender.StartStream(ctx, chatID, replyTo)
}

// NotifyStatus delegates to the webchat or console channel.
func (m *Manager) NotifyStatus(ctx context.Context, chatID string, event channel.StatusEvent) error {
	if chatID == "console" {
		if con := m.GetConsole(); con != nil {
			return con.NotifyStatus(ctx, chatID, event)
		}
	}
	wc := m.GetWebChat()
	if wc == nil {
		return nil // no web chat running
	}
	return wc.NotifyStatus(ctx, chatID, event)
}

// PrompterFor returns a PromptAsker for the given chatID by delegating to the
// appropriate running channel. Implements interact.PromptAskerFactory.
func (m *Manager) PrompterFor(chatID string) interact.PromptAsker {
	// Try console first (chatID == "console").
	if con := m.GetConsole(); con != nil {
		if p := con.PrompterFor(chatID); p != nil {
			return p
		}
	}
	// Try webchat (chatID starts with "web:").
	if wc := m.GetWebChat(); wc != nil {
		if p := wc.PrompterFor(chatID); p != nil {
			return p
		}
	}
	// Try Telegram channels (numeric chatID).
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, mc := range m.running {
		if mc.tg != nil {
			if p := mc.tg.PrompterFor(chatID); p != nil {
				return p
			}
		}
	}
	return nil
}

// GetWebChat returns the webchat.Channel if a web channel instance is running.
func (m *Manager) GetWebChat() *webchat.Channel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, mc := range m.running {
		if mc.web != nil {
			return mc.web
		}
	}
	return nil
}

// FiberUpgradeCheck returns a Fiber middleware that validates WebSocket upgrades
// by delegating to the running webchat channel instance.
func (m *Manager) FiberUpgradeCheck() fiber.Handler {
	return func(c *fiber.Ctx) error {
		wc := m.GetWebChat()
		if wc == nil {
			return fiber.ErrServiceUnavailable
		}
		return wc.FiberUpgradeCheck()(c)
	}
}

// FiberHandler returns a Fiber WebSocket handler that delegates to the running webchat channel instance.
func (m *Manager) FiberHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		wc := m.GetWebChat()
		if wc == nil {
			return fiber.ErrServiceUnavailable
		}
		return wc.FiberHandler()(c)
	}
}

// --- internal helpers ---

func (m *Manager) startInstance(parentCtx context.Context, instance domain.ChannelInstance) error {
	m.mu.RLock()
	_, alreadyRunning := m.running[instance.ID]
	m.mu.RUnlock()
	if alreadyRunning {
		return fmt.Errorf("channel instance %s is already running", instance.ID)
	}

	instanceCtx, cancel := context.WithCancel(parentCtx)
	done := make(chan struct{})
	mc := &ManagedChannel{
		Instance: instance,
		cancel:   cancel,
		done:     done,
	}

	var startFn func(context.Context, channel.MessageHandler) error

	switch instance.Type {
	case domain.ChannelTypeTelegram:
		tg, err := m.createTelegramChannel(instance)
		if err != nil {
			cancel()
			return fmt.Errorf("creating telegram channel for %s: %w", instance.ID, err)
		}
		tg.SetInstanceID(instance.ID)
		tg.SetUserResolver(m.userResolver)
		tg.SetStore(m.store)
		if m.transcriber != nil {
			tg.SetTranscriber(m.transcriber)
		}
		if m.commandRegistrar != nil {
			m.commandRegistrar(tg)
		}
		mc.tg = tg
		startFn = tg.Start

	case domain.ChannelTypeWeb:
		web := webchat.New(m.logger)
		web.SetInstanceID(instance.ID)
		mc.web = web
		startFn = web.Start

	case domain.ChannelTypeDiscord:
		dc, err := m.createDiscordChannel(instance)
		if err != nil {
			cancel()
			return fmt.Errorf("creating discord channel for %s: %w", instance.ID, err)
		}
		dc.SetInstanceID(instance.ID)
		dc.SetUserResolver(m.userResolver)
		mc.discord = dc
		startFn = dc.Start

	case domain.ChannelTypeConsole:
		con := consolech.New(m.logger)
		con.SetInstanceID(instance.ID)
		if m.consoleUserID != "" {
			con.SetUserID(m.consoleUserID)
		}
		if m.consoleStatusProv != nil {
			con.SetStatusProvider(m.consoleStatusProv)
		}
		if m.consoleCompactFn != nil {
			con.SetCompactFunc(m.consoleCompactFn)
		}
		if m.consoleOnExit != nil {
			con.SetOnExit(m.consoleOnExit)
		}
		mc.console = con
		startFn = con.Start

	default:
		cancel()
		return fmt.Errorf("unsupported channel type %q for instance %s", instance.Type, instance.ID)
	}

	m.mu.Lock()
	m.running[instance.ID] = mc
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(done)
		m.logger.Info("channel instance starting",
			zap.String("id", instance.ID), zap.String("type", instance.Type))
		if err := startFn(instanceCtx, m.handler); err != nil && instanceCtx.Err() == nil {
			m.logger.Error("channel instance error",
				zap.String("id", instance.ID), zap.Error(err))
		}
		m.mu.Lock()
		delete(m.running, instance.ID)
		m.mu.Unlock()
		m.logger.Info("channel instance goroutine exited", zap.String("id", instance.ID))
	}()

	return nil
}

func (m *Manager) isSupportedType(channelType string) bool {
	switch channelType {
	case domain.ChannelTypeTelegram, domain.ChannelTypeWeb, domain.ChannelTypeDiscord, domain.ChannelTypeConsole:
		return true
	}
	return false
}

func (m *Manager) restartInstance(ctx context.Context, instance domain.ChannelInstance) error {
	m.logger.Info("restarting channel instance", zap.String("id", instance.ID))
	m.StopInstance(instance.ID)
	return m.startInstance(ctx, instance)
}

func (m *Manager) createTelegramChannel(instance domain.ChannelInstance) (*telegram.Channel, error) {
	var (
		token      string
		allowedIDs []int64
		debounce   time.Duration
		rateLimit  int
		rateWindow time.Duration
	)

	if instance.Source == domain.ChannelSourceConfig {
		// Config-sourced: token and settings come from config.toml or hot-reload.
		m.configTokenMu.RLock()
		token = m.configToken
		m.configTokenMu.RUnlock()
		allowedIDs = m.configAllowedIDs
		debounce = m.configDebounce
		rateLimit = m.configRateLimit
		rateWindow = m.configRateWindow
	} else {
		// Dashboard-sourced: decrypt config JSON and parse.
		configJSON := instance.Config
		if m.cfgStore != nil && m.cfgStore.EncryptionEnabled() {
			var err error
			configJSON, err = m.cfgStore.Decrypt(configJSON)
			if err != nil {
				return nil, fmt.Errorf("decrypting channel config: %w", err)
			}
		}

		var tgCfg TelegramInstanceConfig
		if err := json.Unmarshal([]byte(configJSON), &tgCfg); err != nil {
			return nil, fmt.Errorf("parsing channel config JSON: %w", err)
		}

		token = tgCfg.Token
		allowedIDs = tgCfg.AllowedIDs
		if tgCfg.DebounceWindow != "" {
			if d, err := time.ParseDuration(tgCfg.DebounceWindow); err == nil {
				debounce = d
			}
		}
		if tgCfg.RateWindow != "" {
			if d, err := time.ParseDuration(tgCfg.RateWindow); err == nil {
				rateWindow = d
			}
		}
		rateLimit = tgCfg.RateLimit
	}

	if token == "" {
		return nil, fmt.Errorf("empty token for channel instance %s", instance.ID)
	}

	clearFn := telegram.ClearFunc(m.clearFn)
	tg, err := telegram.New(token, allowedIDs, clearFn, debounce, m.httpClient, m.logger)
	if err != nil {
		return nil, err
	}

	if rateLimit > 0 {
		if rateWindow == 0 {
			rateWindow = time.Minute
		}
		tg.SetRateLimiter(ratelimit.New(rateLimit, rateWindow))
	}

	return tg, nil
}

func (m *Manager) createDiscordChannel(instance domain.ChannelInstance) (*discordch.Channel, error) {
	configJSON := instance.Config
	if m.cfgStore != nil && m.cfgStore.EncryptionEnabled() {
		var err error
		configJSON, err = m.cfgStore.Decrypt(configJSON)
		if err != nil {
			return nil, fmt.Errorf("decrypting discord config: %w", err)
		}
	}

	var dcCfg DiscordInstanceConfig
	if err := json.Unmarshal([]byte(configJSON), &dcCfg); err != nil {
		return nil, fmt.Errorf("parsing discord config JSON: %w", err)
	}

	if dcCfg.Token == "" {
		return nil, fmt.Errorf("empty token for discord instance %s", instance.ID)
	}

	clearFn := discordch.ClearFunc(m.clearFn)
	dc, err := discordch.New(dcCfg.Token, dcCfg.AllowedChannelIDs, clearFn, m.logger)
	if err != nil {
		return nil, err
	}

	if dcCfg.RateLimit > 0 {
		rateWindow := time.Minute
		if dcCfg.RateWindow != "" {
			if d, err := time.ParseDuration(dcCfg.RateWindow); err == nil {
				rateWindow = d
			}
		}
		dc.SetRateLimiter(ratelimit.New(dcCfg.RateLimit, rateWindow))
	}

	return dc, nil
}

// lookupInstanceForChat finds the channel_instance_id stored in user_channels for this chatID.
func (m *Manager) lookupInstanceForChat(ctx context.Context, chatID string) string {
	id, err := m.store.GetChannelInstanceIDByChat(ctx, chatID)
	if err != nil {
		m.logger.Debug("failed to look up channel instance for chat",
			zap.String("chat_id", chatID), zap.Error(err))
	}
	return id
}

// channelSender returns the StreamingSender for a ManagedChannel.
func channelSender(mc *ManagedChannel) channel.StreamingSender {
	if mc.tg != nil {
		return mc.tg
	}
	if mc.web != nil {
		return mc.web
	}
	if mc.discord != nil {
		return mc.discord
	}
	if mc.console != nil {
		return mc.console
	}
	return nil
}

// firstRunning returns the StreamingSender of the first running instance (for fallback).
func (m *Manager) firstRunning() channel.StreamingSender {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, mc := range m.running {
		return channelSender(mc)
	}
	return nil
}

// senderFor returns the StreamingSender for a chatID, with fallback to any running channel.
func (m *Manager) senderFor(ctx context.Context, chatID string) channel.StreamingSender {
	instanceID := m.lookupInstanceForChat(ctx, chatID)
	if instanceID != "" {
		m.mu.RLock()
		mc, ok := m.running[instanceID]
		m.mu.RUnlock()
		if ok {
			return channelSender(mc)
		}
	}
	return m.firstRunning()
}

package slack

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	"github.com/iulita-ai/iulita/internal/bookmark"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/ratelimit"
	"github.com/iulita-ai/iulita/internal/storage"
)

// ClearFunc is called to clear conversation history for a chat.
type ClearFunc func(ctx context.Context, chatID string) error

// chatMeta stores the mapping from composite chatID to real Slack coordinates.
type chatMeta struct {
	channelID    string    // real Slack channel ID (C..., D...)
	threadTS     string    // parent thread timestamp (empty for DMs)
	userID       string    // Slack user ID
	locale       string    // user locale captured at message build time
	inboundTS    string    // last incoming user message TS (target for reactions)
	skipBookmark bool      // remember skill was used this turn, skip bookmark button
	lastUsed     time.Time // for TTL eviction
}

// Channel implements channel.InputChannel for Slack using Socket Mode.
type Channel struct {
	client       *slackapi.Client
	socketClient *socketmode.Client
	botUserID    string

	instanceID     string
	allowedUserIDs map[string]struct{}
	debounceWindow time.Duration
	clearFn        ClearFunc
	rateLimiter    *ratelimit.Limiter
	userResolver   channel.UserResolver
	store          storage.Repository
	rememberSvc    bookmark.Service
	remembers      *rememberState
	prompts        *promptState

	chatMetaMu sync.RWMutex
	chatMetaM  map[string]*chatMeta // composite chatID → meta

	userCacheMu sync.RWMutex
	userCache   map[string]userInfo // slackUserID → cached metadata
	userSF      singleflight.Group  // dedupes concurrent first-time lookups

	logger *zap.Logger
	wg     sync.WaitGroup

	// shutdownCh is closed once when Start observes context cancellation, before
	// wg.Wait(). It unblocks any in-flight interactive prompt (whose handler runs
	// under a detached context) so shutdown does not hang on a pending approval.
	shutdownCh   chan struct{}
	shutdownOnce sync.Once

	// Channel write-permission state (Phase 3 draft-posting). See write.go.
	writeMu     sync.RWMutex
	writeCfg    writeConfig
	postLimiter *ratelimit.Limiter // per-channel hourly post budget (nil = no cap)
	postAPI     writeAPI           // nil → use c.client; injected in tests

	// Observability (Phase 4).
	bus          *eventbus.Bus // nil-safe
	connectCount atomic.Int32  // Socket Mode connect count (for reconnect metric)
}

// SetBus wires the observability event bus (deferred wiring by channelmgr).
func (c *Channel) SetBus(bus *eventbus.Bus) { c.bus = bus }

// New creates a new Slack channel with the given bot and app-level tokens.
func New(botToken, appToken string, allowedUserIDs []string, debounceWindow time.Duration, clearFn ClearFunc, logger *zap.Logger) (*Channel, error) {
	client := slackapi.New(
		botToken,
		slackapi.OptionAppLevelToken(appToken),
	)

	socketClient := socketmode.New(
		client,
		socketmode.OptionLog(nil), // suppress default logger; we use zap
	)

	// Verify credentials and get bot user ID.
	authResp, err := client.AuthTest()
	if err != nil {
		return nil, fmt.Errorf("slack auth test failed: %w", err)
	}

	allowed := make(map[string]struct{}, len(allowedUserIDs))
	for _, id := range allowedUserIDs {
		allowed[id] = struct{}{}
	}

	return &Channel{
		client:         client,
		socketClient:   socketClient,
		botUserID:      authResp.UserID,
		allowedUserIDs: allowed,
		debounceWindow: debounceWindow,
		clearFn:        clearFn,
		remembers:      newRememberState(),
		prompts:        newPromptState(),
		chatMetaM:      make(map[string]*chatMeta),
		userCache:      make(map[string]userInfo),
		shutdownCh:     make(chan struct{}),
		logger:         logger.With(zap.String("channel", "slack")),
	}, nil
}

// SetInstanceID sets the channel instance ID.
func (c *Channel) SetInstanceID(id string) { c.instanceID = id }

// SetRateLimiter attaches a per-chat rate limiter.
func (c *Channel) SetRateLimiter(rl *ratelimit.Limiter) { c.rateLimiter = rl }

// SetUserResolver attaches a user resolver.
func (c *Channel) SetUserResolver(ur channel.UserResolver) { c.userResolver = ur }

// SetStore attaches a storage repository for locale lookup.
func (c *Channel) SetStore(s storage.Repository) { c.store = s }

// Start connects to Slack via Socket Mode and processes events until ctx is canceled.
func (c *Channel) Start(ctx context.Context, handler channel.MessageHandler) error {
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		c.cleanupChatMeta(ctx)
	}()
	// Remember cleanup runs unconditionally — the map stays empty when
	// bookmark service is not wired, so the goroutine is harmless.
	go func() {
		defer c.wg.Done()
		c.remembers.startCleanup(ctx)
	}()

	debounce := newDebouncer(c.debounceWindow, func(msg channel.IncomingMessage) {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.processMessage(ctx, handler, msg)
		}()
	})

	// Run the socket mode client in a background goroutine.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if err := c.socketClient.RunContext(ctx); err != nil && ctx.Err() == nil {
			c.logger.Error("slack socket mode error", zap.Error(err))
		}
	}()

	c.logger.Info("slack bot connected",
		zap.String("instance_id", c.instanceID),
		zap.String("bot_user_id", c.botUserID))

	// drain unblocks in-flight prompts (detached ctx) BEFORE waiting on goroutines,
	// then flushes debounced messages and waits for all workers. Must run on every
	// exit path, including the socketmode Events channel closing on its own.
	drain := func() {
		c.shutdownOnce.Do(func() { close(c.shutdownCh) })
		debounce.flushAll()
		c.wg.Wait()
	}

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("slack: shutting down, flushing debouncer")
			drain()
			return ctx.Err()

		case evt, ok := <-c.socketClient.Events:
			if !ok {
				// socketmode closes Events on ctx-cancel AND on unrecoverable
				// socket error. Run the same shutdown as ctx.Done so prompts don't
				// hang and workers don't leak; return a non-nil error when the
				// socket died while ctx was still alive so the manager can restart.
				c.logger.Info("slack: socket mode events channel closed")
				drain()
				if err := ctx.Err(); err != nil {
					return err
				}
				return fmt.Errorf("slack: socket mode events channel closed unexpectedly")
			}
			c.handleSocketEvent(ctx, evt, debounce)
		}
	}
}

// handleSocketEvent dispatches a Socket Mode event to the appropriate handler.
func (c *Channel) handleSocketEvent(ctx context.Context, evt socketmode.Event, debounce *debouncer) {
	switch evt.Type {
	case socketmode.EventTypeConnected:
		// Count reconnects (every Connected after the first) for observability.
		if c.connectCount.Add(1) > 1 && c.bus != nil {
			c.bus.Publish(ctx, eventbus.Event{Type: eventbus.SlackReconnect, Payload: eventbus.SlackReconnectPayload{
				InstanceID: c.instanceID,
			}})
		}

	case socketmode.EventTypeEventsAPI:
		eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		c.socketClient.Ack(*evt.Request)
		c.handleEventsAPI(ctx, eventsAPIEvent, debounce)

	case socketmode.EventTypeInteractive:
		callback, ok := evt.Data.(slackapi.InteractionCallback)
		if !ok {
			return
		}
		c.socketClient.Ack(*evt.Request)
		c.handleInteraction(callback)

	case socketmode.EventTypeSlashCommand:
		cmd, ok := evt.Data.(slackapi.SlashCommand)
		if !ok {
			return
		}
		c.handleSlashCommand(ctx, evt, cmd)
	}
}

// handleEventsAPI processes Events API events (messages, app mentions).
func (c *Channel) handleEventsAPI(ctx context.Context, event slackevents.EventsAPIEvent, debounce *debouncer) {
	if event.Type != slackevents.CallbackEvent {
		return
	}
	switch ev := event.InnerEvent.Data.(type) {
	case *slackevents.MessageEvent:
		c.handleMessage(ctx, ev, debounce)
	case *slackevents.AppMentionEvent:
		c.handleAppMention(ctx, ev, debounce)
	}
}

// handleMessage processes a regular message event.
func (c *Channel) handleMessage(ctx context.Context, ev *slackevents.MessageEvent, debounce *debouncer) {
	// Ignore bot's own messages, message edits, and other subtypes.
	if ev.User == c.botUserID || ev.User == "" || ev.SubType != "" {
		return
	}

	// Check allowed users.
	if len(c.allowedUserIDs) > 0 {
		if _, ok := c.allowedUserIDs[ev.User]; !ok {
			return
		}
	}

	// Only respond to DMs (D channels) — ignore regular channel messages without @mention.
	if !strings.HasPrefix(ev.Channel, "D") {
		return
	}

	msg := c.buildIncomingMessage(ctx, ev.Channel, ev.User, ev.Text, ev.TimeStamp, "")
	if msg == nil {
		return
	}
	debounce.add(*msg)
}

// handleAppMention processes an @mention event in channels.
func (c *Channel) handleAppMention(ctx context.Context, ev *slackevents.AppMentionEvent, debounce *debouncer) {
	if ev.User == c.botUserID || ev.User == "" {
		return
	}

	if len(c.allowedUserIDs) > 0 {
		if _, ok := c.allowedUserIDs[ev.User]; !ok {
			return
		}
	}

	// Strip the @mention prefix from the text.
	text := strings.TrimSpace(strings.Replace(ev.Text, "<@"+c.botUserID+">", "", 1))

	msg := c.buildIncomingMessage(ctx, ev.Channel, ev.User, text, ev.TimeStamp, ev.ThreadTimeStamp)
	if msg == nil {
		return
	}
	debounce.add(*msg)
}

// buildIncomingMessage creates an IncomingMessage from Slack event data.
func (c *Channel) buildIncomingMessage(ctx context.Context, slackChannel, slackUser, text, messageTS, threadTS string) *channel.IncomingMessage {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	// Determine threading: in public channels reply in-thread (starting one under
	// the user's message if none exists); DMs have no thread. The thread is part
	// of the conversation identity (see composeChatID), so it must be resolved
	// before the chatID is built.
	effectiveThreadTS := threadTS
	if !strings.HasPrefix(slackChannel, "D") && effectiveThreadTS == "" {
		effectiveThreadTS = messageTS
	}

	// Handle /clear typed as message text.
	tag := i18n.ResolveLocale(c.lookupLocale(ctx, slackUser), "")
	if strings.TrimSpace(text) == "/clear" {
		if c.clearFn != nil {
			chatID := c.composeChatID(slackChannel, slackUser, effectiveThreadTS)
			if err := c.clearFn(ctx, chatID); err != nil {
				c.logger.Error("slack: failed to clear history", zap.Error(err))
			}
			c.client.PostMessage(slackChannel, //nolint:errcheck,gosec
				slackapi.MsgOptionText(i18n.Tl(tag, "ChatHistoryCleared"), false))
		}
		return nil
	}

	// Rate limit check.
	chatID := c.composeChatID(slackChannel, slackUser, effectiveThreadTS)
	if c.rateLimiter != nil && !c.rateLimiter.Allow(chatID) {
		c.client.PostEphemeral(slackChannel, slackUser, //nolint:errcheck,gosec
			slackapi.MsgOptionText(i18n.Tl(tag, "ChatRateLimited"), false))
		return nil
	}

	// Resolve user identity.
	msg := &channel.IncomingMessage{
		ChatID:            chatID,
		UserID:            slackUser,
		ChannelInstanceID: c.instanceID,
		Text:              text,
		Caps:              channel.CapStreaming | channel.CapMarkdown | channel.CapButtons | channel.CapReactions,
	}

	// Look up username (cached to avoid blocking the event loop on every msg).
	if name, lang, ok := c.lookupUser(slackUser); ok {
		msg.UserName = name
		msg.LanguageCode = lang
	}

	if c.userResolver != nil {
		resolvedID, resolveErr := c.userResolver.ResolveUser(ctx, "slack", msg.UserID, msg.UserName, chatID)
		if resolveErr != nil {
			c.logger.Warn("slack: user resolution failed", zap.Error(resolveErr))
			tag := i18n.ResolveLocale("", msg.LanguageCode)
			c.client.PostEphemeral(slackChannel, slackUser, //nolint:errcheck,gosec
				slackapi.MsgOptionText(i18n.Tl(tag, "ChatRegistrationNotAllowed"), false))
			return nil
		}
		msg.ResolvedUserID = resolvedID
	}

	// Look up stored locale.
	if c.store != nil {
		if locale, locErr := c.store.GetChannelLocale(ctx, "slack", slackUser); locErr == nil {
			msg.Locale = locale
		}
	}

	// Store chat meta for routing responses. inboundTS is the user's message
	// timestamp so reactions land on the user's message, not the bot's.
	meta := &chatMeta{
		channelID: slackChannel,
		threadTS:  effectiveThreadTS,
		userID:    slackUser,
		locale:    msg.Locale,
		inboundTS: messageTS,
	}
	c.storeChatMeta(chatID, meta)
	// Persist routing coordinates so proactive delivery survives cache eviction
	// (1h TTL) and restarts. Runs on a tracked background goroutine so inbound
	// handling never blocks on the DB write.
	c.persistRoute(chatID, meta)

	return msg
}

// processMessage is called after debouncing to handle the merged message.
// Caller must manage wg.Add/Done.
func (c *Channel) processMessage(parentCtx context.Context, handler channel.MessageHandler, msg channel.IncomingMessage) {
	meta := c.getChatMeta(msg.ChatID)

	// Add hourglass reaction on the user's incoming message as typing indicator.
	if meta != nil && meta.inboundTS != "" {
		c.addReaction("hourglass_flowing_sand", meta.channelID, meta.inboundTS)
	}

	handlerCtx := context.WithoutCancel(parentCtx)
	response, err := handler(handlerCtx, msg)

	if meta != nil && meta.inboundTS != "" {
		c.removeReaction("hourglass_flowing_sand", meta.channelID, meta.inboundTS)
	}

	if err != nil {
		c.logger.Error("slack: handler error", zap.Error(err), zap.String("chat_id", msg.ChatID))
		tag := i18n.ResolveLocale(msg.Locale, msg.LanguageCode)
		response = i18n.Tl(tag, "CommonErrorResponse")
	}

	// Check if remember skill was used this turn (skip bookmark button if so).
	skipBookmark := false
	if meta != nil {
		c.chatMetaMu.Lock()
		skipBookmark = meta.skipBookmark
		meta.skipBookmark = false // reset for next turn
		c.chatMetaMu.Unlock()
	}

	if c.rememberSvc != nil && response != "" && err == nil && !skipBookmark && msg.ResolvedUserID != "" {
		c.sendResponseWithBookmark(parentCtx, msg.ChatID, response, msg.ResolvedUserID)
		return
	}
	c.sendResponse(parentCtx, msg.ChatID, response)
}

// handleInteraction processes Block Kit interactive components.
func (c *Channel) handleInteraction(callback slackapi.InteractionCallback) {
	for _, action := range callback.ActionCallback.BlockActions {
		// Try bookmark first.
		if c.rememberSvc != nil && c.handleBookmarkCallback(action.ActionID, callback.User.ID) {
			return
		}
		// Try interactive prompt.
		if c.handlePromptCallback(action.ActionID, action.Value) {
			return
		}
	}
}

// handleSlashCommand processes slash commands.
func (c *Channel) handleSlashCommand(ctx context.Context, evt socketmode.Event, cmd slackapi.SlashCommand) {
	switch cmd.Command {
	case "/clear":
		tag := i18n.ResolveLocale(c.lookupLocale(ctx, cmd.UserID), "")
		// Channel conversations are thread-scoped, but a slash command carries no
		// thread_ts, so we cannot target the active thread. Rather than clear an
		// always-empty base scope and falsely report success, guide the user to
		// type /clear inside the thread. DMs are a single scope, so they clear.
		if !strings.HasPrefix(cmd.ChannelID, "D") {
			c.socketClient.Ack(*evt.Request, map[string]interface{}{
				"text": i18n.Tl(tag, "ChatClearInThreadHint"),
			})
			return
		}
		chatID := c.composeChatID(cmd.ChannelID, cmd.UserID, "")
		if c.clearFn != nil {
			if err := c.clearFn(ctx, chatID); err != nil {
				c.logger.Error("slack: /clear failed", zap.Error(err))
				c.socketClient.Ack(*evt.Request, map[string]interface{}{
					"text": i18n.Tl(tag, "ChatHistoryClearFailed"),
				})
				return
			}
		}
		c.socketClient.Ack(*evt.Request, map[string]interface{}{
			"text": i18n.Tl(tag, "ChatHistoryCleared"),
		})
	default:
		c.socketClient.Ack(*evt.Request)
	}
}

// lookupLocale resolves a stored locale for the given Slack user (empty on miss).
func (c *Channel) lookupLocale(ctx context.Context, slackUser string) string {
	if c.store == nil || slackUser == "" {
		return ""
	}
	loc, err := c.store.GetChannelLocale(ctx, "slack", slackUser)
	if err != nil {
		return ""
	}
	return loc
}

// --- SendMessage / StartStream (channel.StreamingSender) ---

// SendMessage sends a proactive message to a Slack chat. Routing is recovered
// from the persisted route or the chatID itself, so delivery works even when the
// in-memory cache was evicted or lost on restart.
func (c *Channel) SendMessage(ctx context.Context, chatID, text string) error {
	meta := c.resolveMeta(ctx, chatID)
	if meta == nil {
		return fmt.Errorf("invalid slack chat id %q", chatID)
	}

	mrkdwn := ToMrkdwn(text)
	chunks := splitMessage(mrkdwn, maxMessageLen)
	for _, chunk := range chunks {
		opts := []slackapi.MsgOption{
			slackapi.MsgOptionText(chunk, false),
			slackapi.MsgOptionDisableLinkUnfurl(),
		}
		if meta.threadTS != "" {
			opts = append(opts, slackapi.MsgOptionTS(meta.threadTS))
		}
		if _, _, err := c.client.PostMessage(meta.channelID, opts...); err != nil {
			c.logger.Error("slack: failed to send message", zap.Error(err))
			return err
		}
	}
	return nil
}

// streamUpdate edits a streamed message, logging (not dropping) failures so the
// write layer can observe errors like not_in_channel / channel_not_found.
func (c *Channel) streamUpdate(channelID, ts string, opts ...slackapi.MsgOption) {
	if _, _, _, err := c.client.UpdateMessage(channelID, ts, opts...); err != nil {
		c.logger.Warn("slack: stream edit failed", zap.String("channel", channelID), zap.Error(err))
	}
}

// streamPost posts a follow-up chunk during streaming, logging failures.
func (c *Channel) streamPost(channelID string, opts ...slackapi.MsgOption) {
	if _, _, err := c.client.PostMessage(channelID, opts...); err != nil {
		c.logger.Warn("slack: stream post failed", zap.String("channel", channelID), zap.Error(err))
	}
}

// StartStream sends an initial placeholder and returns edit/done functions.
//
//nolint:gocritic // unnamedResult: returning two anonymous funcs is idiomatic for stream API
func (c *Channel) StartStream(ctx context.Context, chatID string, _ int) (func(string), func(string), error) {
	meta := c.resolveMeta(ctx, chatID)
	if meta == nil {
		return nil, nil, fmt.Errorf("invalid slack chat id %q", chatID)
	}

	opts := []slackapi.MsgOption{
		slackapi.MsgOptionText("...", false),
	}
	if meta.threadTS != "" {
		opts = append(opts, slackapi.MsgOptionTS(meta.threadTS))
	}

	_, ts, err := c.client.PostMessage(meta.channelID, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("sending initial slack message: %w", err)
	}

	var lastEditNs atomic.Int64
	var done atomic.Bool

	editFn := func(text string) {
		if done.Load() {
			return
		}
		now := time.Now().UnixNano()
		if time.Duration(now-lastEditNs.Load()) < 1500*time.Millisecond {
			return
		}
		truncated := truncateRunes(text, maxMessageLen)
		c.streamUpdate(meta.channelID, ts, slackapi.MsgOptionText(truncated, false))
		lastEditNs.Store(time.Now().UnixNano())
	}

	doneFn := func(text string) {
		done.Store(true)
		mrkdwn := ToMrkdwn(text)
		if len(mrkdwn) <= maxMessageLen {
			c.streamUpdate(meta.channelID, ts, slackapi.MsgOptionText(mrkdwn, false))
		} else {
			chunks := splitMessage(mrkdwn, maxMessageLen)
			if len(chunks) > 0 {
				c.streamUpdate(meta.channelID, ts, slackapi.MsgOptionText(chunks[0], false))
				for _, chunk := range chunks[1:] {
					sendOpts := []slackapi.MsgOption{slackapi.MsgOptionText(chunk, false)}
					if meta.threadTS != "" {
						sendOpts = append(sendOpts, slackapi.MsgOptionTS(meta.threadTS))
					}
					c.streamPost(meta.channelID, sendOpts...)
				}
			}
		}
	}

	return editFn, doneFn, nil
}

// StartStreamWithBookmark opens a streaming session with a bookmark button on completion.
//
//nolint:gocritic // unnamedResult: returning two anonymous funcs is idiomatic for stream API
func (c *Channel) StartStreamWithBookmark(ctx context.Context, chatID string, replyTo int, userID string) (func(string), func(string), error) {
	meta := c.resolveMeta(ctx, chatID)
	if meta == nil {
		return nil, nil, fmt.Errorf("invalid slack chat id %q", chatID)
	}

	if c.rememberSvc == nil {
		return c.StartStream(ctx, chatID, replyTo)
	}

	opts := []slackapi.MsgOption{
		slackapi.MsgOptionText("...", false),
	}
	if meta.threadTS != "" {
		opts = append(opts, slackapi.MsgOptionTS(meta.threadTS))
	}

	_, ts, err := c.client.PostMessage(meta.channelID, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("sending initial slack message: %w", err)
	}

	var lastEditNs atomic.Int64
	var doneBool atomic.Bool

	editFn := func(text string) {
		if doneBool.Load() {
			return
		}
		now := time.Now().UnixNano()
		if time.Duration(now-lastEditNs.Load()) < 1500*time.Millisecond {
			return
		}
		truncated := truncateRunes(text, maxMessageLen)
		c.streamUpdate(meta.channelID, ts, slackapi.MsgOptionText(truncated, false))
		lastEditNs.Store(time.Now().UnixNano())
	}

	doneFn := func(text string) {
		doneBool.Store(true)
		mrkdwn := ToMrkdwn(text)

		nonce := generateNonce()
		actionID := "remember:" + nonce

		btn := slackapi.NewButtonBlockElement(actionID, "save",
			slackapi.NewTextBlockObject("plain_text", "💾 Save", false, false))

		// Split into section blocks if text is long.
		textChunks := splitMessage(mrkdwn, maxMessageLen)
		blocks := make([]slackapi.Block, 0, len(textChunks)+1)
		for _, chunk := range textChunks {
			blocks = append(blocks, slackapi.NewSectionBlock(
				slackapi.NewTextBlockObject("mrkdwn", chunk, false, false),
				nil, nil,
			))
		}
		blocks = append(blocks, slackapi.NewActionBlock("bookmark_actions", btn))

		c.streamUpdate(meta.channelID, ts, slackapi.MsgOptionBlocks(blocks...))

		c.remembers.store(actionID, &rememberEntry{
			slackChannel: meta.channelID,
			slackUserID:  meta.userID,
			messageTS:    ts,
			content:      text,
			chatID:       chatID,
			userID:       userID,
			locale:       meta.locale,
			createdAt:    time.Now(),
		})
	}

	return editFn, doneFn, nil
}

// NotifyStatus sends processing status updates. Uses reactions on the user's
// incoming message as a typing indicator.
func (c *Channel) NotifyStatus(_ context.Context, chatID string, event channel.StatusEvent) error {
	// skip_bookmark must be handled even if there's no inbound TS to react on.
	if event.Type == "skip_bookmark" {
		c.chatMetaMu.Lock()
		if m, ok := c.chatMetaM[chatID]; ok {
			m.skipBookmark = true
		}
		c.chatMetaMu.Unlock()
		return nil
	}

	meta := c.getChatMeta(chatID)
	if meta == nil || meta.inboundTS == "" {
		return nil
	}

	switch event.Type {
	case "processing":
		c.addReaction("hourglass_flowing_sand", meta.channelID, meta.inboundTS)
	case "stream_start":
		c.removeReaction("hourglass_flowing_sand", meta.channelID, meta.inboundTS)
	case "error":
		c.addReaction("x", meta.channelID, meta.inboundTS)
	}

	return nil
}

// --- internal helpers ---

// composeChatID creates a routable chatID.
// All Slack chatIDs are prefixed with "slack:" so NotifyStatus prefix-based
// routing matches DMs as well as public/private channels.
//
//   - DMs (channel starts with D): "slack:<channelID>" — DMs are a single 1:1
//     conversation, so the channel ID alone identifies it (no thread).
//   - Channels: "slack:<channelID>:<userID>:<threadTS>" — each thread is its own
//     conversation, so the thread timestamp is part of the identity. This keeps
//     per-turn state (threadTS, inboundTS, skipBookmark) from colliding between
//     concurrent threads of the same user in the same channel, and routes
//     status/streaming/prompts to the correct thread. When threadTS is empty the
//     suffix is omitted (falls back to a channel-user scope).
func (c *Channel) composeChatID(slackChannel, slackUser, threadTS string) string {
	if strings.HasPrefix(slackChannel, "D") {
		return "slack:" + slackChannel
	}
	base := "slack:" + slackChannel + ":" + slackUser
	if threadTS != "" {
		return base + ":" + threadTS
	}
	return base
}

func (c *Channel) storeChatMeta(chatID string, meta *chatMeta) {
	meta.lastUsed = time.Now()
	c.chatMetaMu.Lock()
	c.chatMetaM[chatID] = meta
	c.chatMetaMu.Unlock()
}

// storeChatMetaIfAbsent inserts meta only if no entry exists, returning whichever
// entry is now current. Used by resolveMeta when re-hydrating from the persisted
// route: a route carries only channelID/threadTS/userID/locale, so it must never
// clobber a concurrently-stored inbound entry that has the richer inboundTS /
// skipBookmark / a fresher threadTS (lost-update guard).
func (c *Channel) storeChatMetaIfAbsent(chatID string, meta *chatMeta) *chatMeta {
	meta.lastUsed = time.Now()
	c.chatMetaMu.Lock()
	defer c.chatMetaMu.Unlock()
	if existing, ok := c.chatMetaM[chatID]; ok {
		return existing
	}
	c.chatMetaM[chatID] = meta
	return meta
}

// cleanupChatMeta evicts stale chat meta entries every 10 minutes.
func (c *Channel) cleanupChatMeta(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.chatMetaMu.Lock()
			for k, m := range c.chatMetaM {
				if time.Since(m.lastUsed) > 1*time.Hour {
					delete(c.chatMetaM, k)
				}
			}
			c.chatMetaMu.Unlock()
			c.sweepStaleRoutes(ctx)
		}
	}
}

// slackRouteRetention bounds how long a persisted route survives without a new
// inbound. Thread-scoped chatIDs mint one route row per thread, so without a
// sweep the table would grow unbounded (unlike chatMeta, which has a TTL).
const slackRouteRetention = 30 * 24 * time.Hour

// sweepStaleRoutes prunes persisted routes for this instance older than the
// retention window. Best-effort: failures are logged, never fatal.
func (c *Channel) sweepStaleRoutes(ctx context.Context) {
	if c.store == nil || c.instanceID == "" {
		return
	}
	cutoff := time.Now().Add(-slackRouteRetention)
	n, err := c.store.DeleteSlackRoutesOlderThan(ctx, c.instanceID, cutoff)
	if err != nil {
		c.logger.Warn("slack: route retention sweep failed", zap.Error(err))
		return
	}
	if n > 0 {
		c.logger.Debug("slack: swept stale routes", zap.Int64("count", n))
	}
}

func (c *Channel) getChatMeta(chatID string) *chatMeta {
	c.chatMetaMu.RLock()
	m := c.chatMetaM[chatID]
	c.chatMetaMu.RUnlock()
	return m
}

// userInfo is a cached entry for Slack user metadata. A failed lookup is cached
// too (with a shorter TTL) so a single unresolvable/rate-limited user does not
// trigger a GetUserInfo call on every message.
type userInfo struct {
	name   string
	lang   string
	ts     time.Time
	failed bool
}

const (
	userCacheTTL         = 30 * time.Minute
	userNegativeCacheTTL = 1 * time.Minute
)

// lookupUser returns the display name and Slack-reported locale for a user,
// caching results for 30 minutes. Concurrent first-time lookups for the same
// user are deduplicated via singleflight so a burst of messages produces at
// most one GetUserInfo call.
//
//nolint:gocritic // unnamedResult: returning two anonymous funcs is idiomatic for stream API
func (c *Channel) lookupUser(slackUser string) (string, string, bool) {
	if slackUser == "" {
		return "", "", false
	}

	c.userCacheMu.RLock()
	if u, ok := c.userCache[slackUser]; ok {
		if u.failed {
			if time.Since(u.ts) < userNegativeCacheTTL {
				c.userCacheMu.RUnlock()
				return "", "", false
			}
		} else if time.Since(u.ts) < userCacheTTL {
			c.userCacheMu.RUnlock()
			return u.name, u.lang, true
		}
	}
	c.userCacheMu.RUnlock()

	v, err, _ := c.userSF.Do(slackUser, func() (interface{}, error) {
		info, err := c.client.GetUserInfo(slackUser)
		if err != nil {
			// Cache the failure briefly so a burst of messages from an
			// unresolvable/rate-limited user does not hammer GetUserInfo.
			c.userCacheMu.Lock()
			c.userCache[slackUser] = userInfo{failed: true, ts: time.Now()}
			c.userCacheMu.Unlock()
			return nil, err
		}
		name := info.Name
		if info.RealName != "" {
			name = info.RealName
		}
		entry := userInfo{name: name, lang: info.Locale, ts: time.Now()}
		c.userCacheMu.Lock()
		c.userCache[slackUser] = entry
		c.userCacheMu.Unlock()
		return entry, nil
	})
	if err != nil {
		return "", "", false
	}
	u, ok := v.(userInfo)
	if !ok {
		return "", "", false
	}
	return u.name, u.lang, true
}

func (c *Channel) addReaction(name, slackChannel, ts string) {
	if ts == "" {
		return
	}
	err := c.client.AddReaction(name, slackapi.ItemRef{
		Channel:   slackChannel,
		Timestamp: ts,
	})
	if err != nil {
		c.logger.Debug("slack: failed to add reaction", zap.String("reaction", name), zap.Error(err))
	}
}

func (c *Channel) removeReaction(name, slackChannel, ts string) {
	if ts == "" {
		return
	}
	err := c.client.RemoveReaction(name, slackapi.ItemRef{
		Channel:   slackChannel,
		Timestamp: ts,
	})
	if err != nil {
		c.logger.Debug("slack: failed to remove reaction", zap.String("reaction", name), zap.Error(err))
	}
}

// sendResponseWithBookmark sends a non-streaming response with a 💾 Save
// button attached so the user can persist the assistant message as a fact.
// Falls back to sendResponse when the bookmark service is not wired.
func (c *Channel) sendResponseWithBookmark(ctx context.Context, chatID, text, resolvedUserID string) {
	if c.rememberSvc == nil {
		c.sendResponse(ctx, chatID, text)
		return
	}

	meta := c.resolveMeta(ctx, chatID)
	if meta == nil {
		c.logger.Warn("slack: unroutable chat id for response", zap.String("chat_id", chatID))
		return
	}

	mrkdwn := ToMrkdwn(text)
	chunks := splitMessage(mrkdwn, maxMessageLen)
	if len(chunks) == 0 {
		return
	}

	// All chunks except the last are sent as plain messages; the last carries
	// the save button so the user can bookmark the full response.
	// On intermediate failure, log and continue so the user still receives the
	// remaining content.
	for _, chunk := range chunks[:len(chunks)-1] {
		opts := []slackapi.MsgOption{
			slackapi.MsgOptionText(chunk, false),
			slackapi.MsgOptionDisableLinkUnfurl(),
		}
		if meta.threadTS != "" {
			opts = append(opts, slackapi.MsgOptionTS(meta.threadTS))
		}
		if _, _, err := c.client.PostMessage(meta.channelID, opts...); err != nil {
			c.logger.Error("slack: failed to send intermediate chunk", zap.Error(err))
		}
	}

	last := chunks[len(chunks)-1]
	nonce := generateNonce()
	actionID := "remember:" + nonce
	btn := slackapi.NewButtonBlockElement(actionID, "save",
		slackapi.NewTextBlockObject("plain_text", "💾 Save", false, false))
	blocks := []slackapi.Block{
		slackapi.NewSectionBlock(
			slackapi.NewTextBlockObject("mrkdwn", last, false, false),
			nil, nil,
		),
		slackapi.NewActionBlock("bookmark_actions", btn),
	}

	opts := []slackapi.MsgOption{
		slackapi.MsgOptionBlocks(blocks...),
		slackapi.MsgOptionDisableLinkUnfurl(),
	}
	if meta.threadTS != "" {
		opts = append(opts, slackapi.MsgOptionTS(meta.threadTS))
	}
	_, ts, err := c.client.PostMessage(meta.channelID, opts...)
	if err != nil {
		c.logger.Error("slack: failed to send response", zap.Error(err))
		return
	}

	c.remembers.store(actionID, &rememberEntry{
		slackChannel: meta.channelID,
		slackUserID:  meta.userID,
		messageTS:    ts,
		content:      text,
		chatID:       chatID,
		userID:       resolvedUserID,
		locale:       meta.locale,
		createdAt:    time.Now(),
	})
}

// sendResponse sends a text response (non-streaming) to the chat.
func (c *Channel) sendResponse(ctx context.Context, chatID, text string) {
	meta := c.resolveMeta(ctx, chatID)
	if meta == nil {
		c.logger.Warn("slack: unroutable chat id for response", zap.String("chat_id", chatID))
		return
	}

	mrkdwn := ToMrkdwn(text)
	chunks := splitMessage(mrkdwn, maxMessageLen)
	for _, chunk := range chunks {
		opts := []slackapi.MsgOption{
			slackapi.MsgOptionText(chunk, false),
			slackapi.MsgOptionDisableLinkUnfurl(),
		}
		if meta.threadTS != "" {
			opts = append(opts, slackapi.MsgOptionTS(meta.threadTS))
		}
		if _, _, err := c.client.PostMessage(meta.channelID, opts...); err != nil {
			c.logger.Error("slack: failed to send response", zap.Error(err))
		}
	}
}

// Package telegrambusiness implements the isolated connected-account service.
// It never grants a peer the account owner's ordinary assistant context.
package telegrambusiness

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Errors identify closed policy, quota and uncertain-outcome failures.
var (
	ErrDenied             = errors.New("business action denied")
	ErrUnavailable        = errors.New("business scope unavailable")
	ErrStale              = errors.New("business revision expired")
	ErrQuota              = errors.New("business quota exhausted")
	ErrLiveWritesDisabled = errors.New("business live actions disabled")
	ErrUnknownOutcome     = errors.New("business action outcome unknown")
	ErrUnsupported        = errors.New("business operation unsupported")
)

// Key identifies one immutable connection on one observed bot instance.
type Key struct {
	InstanceID   string `json:"instance_id"`
	BotID        int64  `json:"bot_id"`
	ConnectionID string `json:"connection_id"`
}

// Scope separates a peer conversation from its owner and every other audience.
type Scope struct {
	Key     Key    `json:"key"`
	OwnerID string `json:"owner_id"`
	PeerID  int64  `json:"peer_id"`
}

// Rights mirrors the fourteen independent account grants; omission means false.
type Rights struct {
	CanReply                   bool `json:"can_reply"`
	CanReadMessages            bool `json:"can_read_messages"`
	CanDeleteSentMessages      bool `json:"can_delete_sent_messages"`
	CanDeleteAllMessages       bool `json:"can_delete_all_messages"`
	CanEditName                bool `json:"can_edit_name"`
	CanEditBio                 bool `json:"can_edit_bio"`
	CanEditProfilePhoto        bool `json:"can_edit_profile_photo"`
	CanEditUsername            bool `json:"can_edit_username"`
	CanChangeGiftSettings      bool `json:"can_change_gift_settings"`
	CanViewGiftsAndStars       bool `json:"can_view_gifts_and_stars"`
	CanConvertGiftsToStars     bool `json:"can_convert_gifts_to_stars"`
	CanTransferAndUpgradeGifts bool `json:"can_transfer_and_upgrade_gifts"`
	CanTransferStars           bool `json:"can_transfer_stars"`
	CanManageStories           bool `json:"can_manage_stories"`
}

// RemoteConnection is verified transport metadata, never an owner registration.
type RemoteConnection struct {
	Key             Key       `json:"key"`
	OwnerTelegramID int64     `json:"owner_telegram_id"`
	OwnerChatID     int64     `json:"owner_chat_id"`
	Enabled         bool      `json:"enabled"`
	Rights          Rights    `json:"rights"`
	ObservedAt      time.Time `json:"observed_at"`
}

// Connection records local authority separately from Telegram's grants.
type Connection struct {
	RemoteConnection
	OwnerID            string     `json:"owner_id"`
	Epoch              uint64     `json:"epoch"`
	ControlUpdateID    int64      `json:"control_update_id"`
	DeliveryGeneration string     `json:"delivery_generation"`
	VerifiedAt         time.Time  `json:"verified_at"`
	Paused             bool       `json:"paused"`
	RestoreGeneration  string     `json:"restore_generation"`
	Quarantined        bool       `json:"quarantined"`
	Policy             PeerPolicy `json:"policy"`
	DispatchActionID   string     `json:"dispatch_action_id,omitempty"`
	OwnerSecurityEpoch uint64     `json:"owner_security_epoch"`
	RuntimeGeneration  string     `json:"runtime_generation"`
	NeedsRefresh       bool       `json:"needs_refresh,omitempty"`
}

// Mode is an explicit local product setting, not a Telegram permission.
type Mode string

// Modes are explicit local processing choices, separate from Telegram rights.
const (
	ModeOff     Mode = "off"
	ModeDraft   Mode = "draft_only"
	ModeConfirm Mode = "confirmed"
	ModeAuto    Mode = "auto"
)

// PeerPolicy requires provider consent before retaining content or generation.
type PeerPolicy struct {
	MediaConsent   bool       `json:"media_consent"`
	MediaProvider  string     `json:"media_provider"`
	Enabled        bool       `json:"enabled"`
	Consent        bool       `json:"consent"`
	Mode           Mode       `json:"mode"`
	Provider       string     `json:"provider"`
	Revision       uint64     `json:"revision"`
	AllowedMethods []Method   `json:"allowed_methods,omitempty"`
	AutoReply      bool       `json:"auto_reply"`
	AutoRules      *AutoRules `json:"auto_rules,omitempty"`
	TokenOnly      bool       `json:"token_only"`
}

// AutoRules is a narrow owner-selected reply policy. Missing rules never allow
// automatic dispatch; daily reply accounting uses the UTC budget day.
type AutoRules struct {
	Classes          []string `json:"classes"`
	Timezone         string   `json:"timezone"`
	StartHour        int      `json:"start_hour"`
	EndHour          int      `json:"end_hour"`
	MaxRepliesPerDay int      `json:"max_replies_per_day"`
}

// Media is a scoped attachment reference; it never itself grants file access.
type Media struct {
	Kind            string `json:"kind"`
	FileID          string `json:"file_id"`
	SHA256          string `json:"sha256,omitempty"`
	Bytes           int64  `json:"bytes"`
	DurationSeconds int    `json:"duration_seconds,omitempty"`
}

// Message retains source revisions and direction in its connection/peer scope.
type Message struct {
	ExtractedText     string    `json:"extracted_text,omitempty"`
	ExtractedProvider string    `json:"extracted_provider,omitempty"`
	MediaGroupID      string    `json:"media_group_id,omitempty"`
	ID                int       `json:"id"`
	SenderID          int64     `json:"sender_id"`
	SenderIsBot       bool      `json:"sender_is_bot"`
	Date              time.Time `json:"date"`
	Revision          int64     `json:"revision"`
	Direction         string    `json:"direction"` // incoming, owner_outgoing, bot_outgoing
	Text              string    `json:"text,omitempty"`
	Media             []Media   `json:"media,omitempty"`
	Deleted           bool      `json:"deleted"`
}

// Peer is connection-local state. No field may be promoted to owner memory.
type Peer struct {
	LastProcessingState  string             `json:"last_processing_state"`
	LastProcessingReason string             `json:"last_processing_reason"`
	LastProcessedAt      time.Time          `json:"last_processed_at"`
	LastMediaOmitted     int                `json:"last_media_omitted"`
	ProcessRevision      uint64             `json:"process_revision"`
	ID                   int64              `json:"id"`
	Policy               PeerPolicy         `json:"policy"`
	Revision             uint64             `json:"revision"`
	LastIncomingAt       time.Time          `json:"last_incoming_at"`
	ReplayAfter          time.Time          `json:"replay_after,omitempty"`
	Messages             []Message          `json:"messages,omitempty"`
	Drafts               map[string]*Draft  `json:"drafts,omitempty"`
	Actions              map[string]*Action `json:"actions,omitempty"`
	Summary              *Summary           `json:"summary,omitempty"`
	AutoRevision         uint64             `json:"auto_revision"`
	AutoClaim            string             `json:"auto_claim,omitempty"`
	OwnerDraftRevision   uint64             `json:"owner_draft_revision"`
}

// Summary covers exact original source revisions, including a UTF-8 byte offset
// into one oversized source. It is derived untrusted content, never owner facts.
type Summary struct {
	Text         string           `json:"text"`
	Sources      []SourceRevision `json:"sources"`
	ThroughID    int              `json:"through_id"`
	ThroughBytes int              `json:"through_bytes"`
	CreatedAt    time.Time        `json:"created_at"`
	BeforeBytes  int              `json:"before_bytes"`
	AfterBytes   int              `json:"after_bytes"`
}

// SourceRevision binds a summary to one exact original message version.
type SourceRevision struct {
	ID       int   `json:"id"`
	Revision int64 `json:"revision"`
}

// DeletionRepository permits strictly erasing known-scope source/derived data
// after account disable/revocation, without granting ordinary reads or writes.
type DeletionRepository interface {
	ApplyTelegramBusinessDeletion(context.Context, Key, string, int64, []int, time.Time) error
}

// Draft is an immutable approved snapshot after any edit invalidates approval.
type Draft struct {
	ID              string    `json:"id"`
	Scope           Scope     `json:"scope"`
	Epoch           uint64    `json:"epoch"`
	ContextRevision uint64    `json:"context_revision"`
	Revision        uint64    `json:"revision"`
	Text            string    `json:"text"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	State           string    `json:"state"`
	ActionID        string    `json:"action_id,omitempty"`
}

// Method is a closed Telegram operation name, not a model-selected raw URL.
type Method string

// Methods enumerate the closed connected-account operation catalog.
const (
	SendText           Method = "sendMessage"
	SendPhoto          Method = "sendPhoto"
	SendDocument       Method = "sendDocument"
	SendVoice          Method = "sendVoice"
	SendAudio          Method = "sendAudio"
	SendVideo          Method = "sendVideo"
	EditText           Method = "editMessageText"
	ReadMessage        Method = "readBusinessMessage"
	DeleteSent         Method = "delete_sent_business_messages"
	DeleteAll          Method = "delete_all_business_messages"
	SetName            Method = "setBusinessAccountName"
	SetBio             Method = "setBusinessAccountBio"
	SetUsername        Method = "setBusinessAccountUsername"
	SetProfilePhoto    Method = "setBusinessAccountProfilePhoto"
	RemoveProfilePhoto Method = "removeBusinessAccountProfilePhoto"
	SetGiftSettings    Method = "setBusinessAccountGiftSettings"
	GetStarBalance     Method = "getBusinessAccountStarBalance"
	GetGifts           Method = "getBusinessAccountGifts"
	ConvertGift        Method = "convertGiftToStars"
	DeleteGift         Method = "deleteBusinessAccountGift"
	UpgradeGift        Method = "upgradeGift"
	TransferGift       Method = "transferGift"
	TransferStars      Method = "transferBusinessAccountStars"
	PostStory          Method = "postStory"
	EditStory          Method = "editStory"
	DeleteStory        Method = "deleteStory"
	RepostStory        Method = "repostStory"
)

// Params is a bounded, canonical JSON object checked against the method schema.
// Unknown fields, nested authority fields and unsupported methods are rejected.
type Params map[string]json.RawMessage

// Action binds the complete exact request to owner approval and live authority.
type Action struct {
	ID              string    `json:"id"`
	Scope           Scope     `json:"scope"`
	Epoch           uint64    `json:"epoch"`
	ContextRevision uint64    `json:"context_revision"`
	PolicyRevision  uint64    `json:"policy_revision"`
	Method          Method    `json:"method"`
	Params          Params    `json:"params"`
	Digest          string    `json:"digest"`
	State           string    `json:"state"`
	ApprovalSteps   int       `json:"approval_steps"`
	RequiredSteps   int       `json:"required_steps"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Receipt         *Receipt  `json:"receipt,omitempty"`
	Failure         string    `json:"failure,omitempty"`
	DraftID         string    `json:"draft_id,omitempty"`
	DraftRevision   uint64    `json:"draft_revision,omitempty"`
	SourceKey       *Key      `json:"source_key,omitempty"`
	SourceEpoch     uint64    `json:"source_epoch,omitempty"`
	ReservedStars   int64     `json:"reserved_stars,omitempty"`
	Automatic       bool      `json:"automatic,omitempty"`
	DispatchAt      time.Time `json:"dispatch_at,omitempty"`
}

// Receipt contains bounded server output, not private contents in an audit log.
type Receipt struct {
	MessageIDs []int           `json:"message_ids,omitempty"`
	StoryID    int             `json:"story_id,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
}

// Audit contains no body, token or raw external error.
type Audit struct {
	At          time.Time `json:"at"`
	Kind        string    `json:"kind"`
	OperationID string    `json:"operation_id,omitempty"`
	Outcome     string    `json:"outcome"`
}

// State is one atomic connection snapshot. Private fields require encryption.
type State struct {
	Connection     Connection           `json:"connection"`
	Peers          map[int64]*Peer      `json:"peers,omitempty"`
	AccountActions map[string]*Action   `json:"account_actions,omitempty"`
	Seen           map[string]time.Time `json:"seen,omitempty"`
	Audit          []Audit              `json:"audit,omitempty"`
}

// Repository callbacks run exactly once in a short write transaction. They must
// perform no network/provider work. New states have Connection.Key initialized;
// Get returns nil for an unknown exact key, with no instance/owner fallback.
type Repository interface {
	List(context.Context, string) ([]State, error)
	Get(context.Context, Key) (*State, error)
	Update(context.Context, Key, func(*State) error) error
}

// Cipher authenticates private state against the serialized exact storage key.
type Cipher interface {
	Seal(plaintext, additionalData []byte) ([]byte, error)
	Open(ciphertext, additionalData []byte) ([]byte, error)
}

// Request is already validated and claimed by the service. Transport adapters
// must serialize Payload(), never omit its business connection or change target.
type Request struct {
	Action Action
}

// API is the only external Telegram capability used by this service.
type API interface {
	GetConnection(context.Context, Key) (RemoteConnection, error)
	Execute(context.Context, Request) (Receipt, error)
}

// Generator receives only this peer's retained content; no private tools/memory.
type Generator interface {
	Generate(context.Context, Scope, []Message) (string, error)
}

// Reservation is opaque to the service. Amounts reserve worst-case model cost.
type Reservation struct {
	ID        string
	ExpiresAt time.Time
}

// BudgetRequest reserves worst-case amounts before any billable provider call.
// Release frees concurrency only; attempts and daily reserved usage are retained.
type BudgetRequest struct {
	Scope        Scope
	Provider     string
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64
	TokenOnly    bool
	At           time.Time
	ExpiresAt    time.Time
}

// Budget reserves finite model concurrency and daily allowance atomically.
type Budget interface {
	Reserve(context.Context, BudgetRequest) (Reservation, error)
	Release(context.Context, Reservation) error
}

// Options leaves every external account operation disabled by default.
type Options struct {
	Observer          Observer
	Media             *MediaVault
	LiveWrites        bool
	Now               func() time.Time
	Budget            Budget
	RestoreGeneration string
	EstimateCost      func(provider string, inputTokens, outputTokens int64) (micros int64, known bool)
	// VerifyMedia proves the referenced existing source/file belongs to the scope.
	VerifyMedia func(context.Context, Scope, string) error
	// Zero disables value-changing intents even when Telegram granted rights.
	FinancialLimits   FinancialLimits
	RuntimeGeneration string
}

// FinancialLimits are explicit per-connection UTC-day bounds, not model tokens.
type FinancialLimits struct {
	DailyOperations int
	DailyStars      int64
}

// Event is a trusted typed intake. DeferProcessing retains the source but
// withholds automatic generation until the bot-wide cursor commits.
type Event struct {
	DeferProcessing bool
	Key             Key
	OwnerID         string
	Generation      string
	UpdateID        int64
	Kind            string
	Connection      *RemoteConnection
	PeerID          int64
	Message         *Message
	DeletedIDs      []int
}

// ConfigureInput is accepted only from the authenticated owner control plane.
type ConfigureInput struct {
	Scope            Scope
	Policy           PeerPolicy
	ExpectedRevision uint64
}

// PrepareInput binds a closed operation and exact parameters to an owner scope.
type PrepareInput struct {
	Scope            Scope
	Method           Method
	Params           Params
	DraftID          string
	ExpectedRevision *uint64
}

// ApprovalInput prevents an approval of changed parameters or a changed revision.
type ApprovalInput struct {
	Scope    Scope
	ActionID string
	Digest   string
	Step     int
}

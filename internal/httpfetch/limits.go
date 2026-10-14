package httpfetch

import "time"

// Limits holds every server-side bound from plan §11. Defaults are design
// decisions, not discovered production settings; the operator may tighten
// most of them via [skills.http] but never loosen consent-critical ones
// beyond their ceiling.
type Limits struct {
	// ConsentThresholdBytes: reading more than this many bytes requires
	// explicit human confirmation. Operator may lower it, never raise it.
	ConsentThresholdBytes int64
	// ConsentThresholdCeiling is the hard maximum the operator may configure.
	ConsentThresholdCeiling int64

	MaxDownloadBytes   int64 // download hard max / transfer wire cap
	MaxUploadBytes     int64 // request body cap (any encoding)
	InlineMaxBytes     int64 // inline response read cap (decoded)
	BodyPreviewBytes   int64 // LLM-facing body preview
	ResultMaxBytes     int64 // total tool JSON result cap
	HeaderBlockBytes   int64 // request/response header block cap
	MaxHeaderFields    int   // max fields per header block
	HeaderPreviewBytes int64 // LLM-facing header preview total
	MaxRedirects       int
	MaxChainSteps      int

	MaxTransfersGlobal int // active network transfers, process-wide
	MaxTransfersUser   int // active network transfers per user
	MaxDownloadWriters int // concurrent download writers, process-wide
	MaxPendingGlobal   int // paused-stream confirmations holding a response
	MaxPendingUser     int

	DialTimeout         time.Duration // proxy dial
	TLSHandshakeTimeout time.Duration // TLS handshake (origin and proxy)
	ResponseHeaderWait  time.Duration // waiting for response headers
	BodyIdleTimeout     time.Duration // max gap between body reads

	RequestTimeout time.Duration // single non-download step
	ChainBudget    time.Duration // sum of non-download steps per run
	DownloadBudget time.Duration // sum of active download time per run
	ConsentWait    time.Duration // per-confirmation wait
	WallTimeout    time.Duration // max whole-operation time

	ArtifactTTL      time.Duration
	ArtifactSweep    time.Duration
	QuotaGlobalBytes int64 // decimal GB per plan
	QuotaUserBytes   int64
	DiskReserveBytes int64
}

// DefaultLimits returns the §11 defaults.
func DefaultLimits() Limits {
	return Limits{
		ConsentThresholdBytes:   100_000_000,
		ConsentThresholdCeiling: 100_000_000,

		MaxDownloadBytes:   1_000_000_000,
		MaxUploadBytes:     16 << 20,
		InlineMaxBytes:     2 << 20,
		BodyPreviewBytes:   32 << 10,
		ResultMaxBytes:     64 << 10,
		HeaderBlockBytes:   64 << 10,
		MaxHeaderFields:    256,
		HeaderPreviewBytes: 16 << 10,
		MaxRedirects:       5,
		MaxChainSteps:      10,

		MaxTransfersGlobal: 4,
		MaxTransfersUser:   2,
		MaxDownloadWriters: 2,
		MaxPendingGlobal:   2,
		MaxPendingUser:     1,

		DialTimeout:         10 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderWait:  30 * time.Second,
		BodyIdleTimeout:     30 * time.Second,

		RequestTimeout: 60 * time.Second,
		ChainBudget:    5 * time.Minute,
		DownloadBudget: 15 * time.Minute,
		ConsentWait:    5 * time.Minute,
		WallTimeout:    35 * time.Minute,

		ArtifactTTL:      24 * time.Hour,
		ArtifactSweep:    15 * time.Minute,
		QuotaGlobalBytes: 5_000_000_000,
		QuotaUserBytes:   1_000_000_000,
		DiskReserveBytes: 1_000_000_000,
	}
}

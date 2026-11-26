package dashboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

const secretaryPolicyVersion = "secretary-v1"

func (s *Server) secretaryProfile() (string, bool) {
	if s.businessProfileFn != nil {
		return s.businessProfileFn()
	}
	return s.businessProvider, s.businessProviderAvailable
}
func secretaryConsentVersion(provider string) string {
	sum := sha256.Sum256([]byte(provider))
	return secretaryPolicyVersion + ":" + hex.EncodeToString(sum[:8])
}
func (s *Server) secretaryConnectionDTO(c business.Connection) fiber.Map {
	provider, available := s.secretaryProfile()
	available = available && s.businessEncryptionEnabled
	out := connectionDTO(c, provider, available)
	out["storage_ready"] = s.businessEncryptionEnabled
	out["processing_consent"] = c.Policy.Consent && provider != "" && c.Policy.Provider == provider
	out["auto_rules"] = c.Policy.AutoRules
	descriptor := ""
	if s.businessSources != nil {
		descriptor = s.businessSources.Descriptor(business.Scope{Key: c.Key, OwnerID: c.OwnerID})
	}
	out["media_provider"] = descriptor
	out["media_available"] = descriptor != "" && s.businessEncryptionEnabled
	out["media_consent"] = c.Policy.MediaConsent && descriptor != "" && c.Policy.MediaProvider == descriptor
	if !s.businessEncryptionEnabled {
		out["state"] = "metadata_only"
		out["mode"] = "off"
		out["processing_consent"] = false
		out["media_consent"] = false
		out["error_code"] = "encryption_required"
	}
	return out
}
func (s *Server) secretaryPeerDTO(c business.Connection, p business.Peer) fiber.Map {
	out := peerDTO(p)
	provider, _ := s.secretaryProfile()
	descriptor := ""
	if s.businessSources != nil {
		descriptor = s.businessSources.Descriptor(business.Scope{Key: c.Key, OwnerID: c.OwnerID, PeerID: p.ID})
	}
	out["processing_consent"] = p.Policy.Consent && provider != "" && p.Policy.Provider == provider && s.businessEncryptionEnabled
	out["media_provider"] = descriptor
	out["media_available"] = descriptor != "" && s.businessEncryptionEnabled
	out["media_consent"] = p.Policy.MediaConsent && descriptor != "" && p.Policy.MediaProvider == descriptor && s.businessEncryptionEnabled
	out["auto_rules"] = p.Policy.AutoRules
	out["last_processing_state"] = p.LastProcessingState
	out["last_processing_reason"] = p.LastProcessingReason
	if !p.LastProcessedAt.IsZero() {
		out["last_processed_at"] = p.LastProcessedAt
	}
	deadline := p.LastIncomingAt.Add(24*time.Hour - 30*time.Second)
	out["reply_available"] = !p.LastIncomingAt.IsZero() && time.Now().UTC().Before(deadline)
	if !p.LastIncomingAt.IsZero() {
		out["reply_until"] = deadline
	}
	return out
}

func (s *Server) registerTelegramBusiness(api fiber.Router) {
	b := api.Group("/telegram-business", s.businessOwner)
	b.Get("/connections", s.businessConnections)
	b.Get("/connections/:id/peers", s.businessPeers)
	b.Get("/connections/:id/actions", s.businessListActions)
	b.Get("/connections/:id/peers/:peer/actions", s.businessListActions)
	b.Post("/connections/:id/media", s.businessUpload)
	b.Post("/connections/:id/peers/:peer/media", s.businessUpload)
	b.Get("/connections/:id/peers/:peer/messages", s.businessMessages)
	b.Put("/connections/:id/policy", s.businessPolicy)
	b.Put("/connections/:id/peers/:peer/policy", s.businessPolicy)
	b.Post("/connections/:id/pause", s.businessPause)
	b.Post("/connections/:id/peers/:peer/pause", s.businessPause)
	b.Post("/connections/:id/purge", s.businessPurge)
	b.Post("/connections/:id/peers/:peer/purge", s.businessPurge)
	b.Get("/connections/:id/export", s.businessExport)
	b.Get("/connections/:id/peers/:peer/export", s.businessExport)
	b.Post("/connections/:id/rebind", s.businessRebind)
	b.Post("/connections/:id/peers/:peer/drafts", s.businessCreateDraft)
	b.Post("/connections/:id/peers/:peer/compact", s.businessCompact)
	b.Put("/drafts/:draft", s.businessEditDraft)
	b.Post("/drafts/:draft/reject", s.businessRejectDraft)
	b.Post("/drafts/:draft/approve", s.businessApproveDraft)
	b.Post("/drafts/:draft/execute", s.businessExecuteDraft)
	b.Post("/connections/:id/actions", s.businessPrepareAction)
	b.Post("/connections/:id/peers/:peer/actions", s.businessPrepareAction)
	b.Get("/connections/:id/actions/:action", s.businessGetAction)
	b.Get("/connections/:id/peers/:peer/actions/:action", s.businessGetAction)
	b.Post("/connections/:id/actions/:action/approve", s.businessApproveAction)
	b.Post("/connections/:id/peers/:peer/actions/:action/approve", s.businessApproveAction)
	b.Post("/connections/:id/actions/:action/execute", s.businessExecuteAction)
	b.Post("/connections/:id/actions/:action/cancel", s.businessCancelAction)
	b.Post("/connections/:id/peers/:peer/actions/:action/cancel", s.businessCancelAction)
	b.Post("/connections/:id/peers/:peer/actions/:action/execute", s.businessExecuteAction)
}

func (s *Server) businessOwner(c *fiber.Ctx) error {
	claims := auth.GetClaims(c)
	if s.authService == nil || s.telegramBusiness == nil || s.store == nil || claims == nil || claims.UserID == "" {
		return businessHTTP(c, 401, "unauthorized")
	}
	user, err := s.store.GetUser(c.UserContext(), claims.UserID)
	if err != nil || user == nil || user.Disabled || user.MustChangePass || claims.SecurityEpoch == 0 || user.SecurityEpoch != claims.SecurityEpoch {
		return businessHTTP(c, 401, "unauthorized")
	}
	c.Set("Cache-Control", "no-store")
	c.Set("Pragma", "no-cache")
	c.Set("X-Content-Type-Options", "nosniff")
	return c.Next()
}
func businessHTTP(c *fiber.Ctx, status int, code string) error {
	return c.Status(status).JSON(fiber.Map{"code": code})
}
func businessError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, business.ErrLiveWritesDisabled):
		return businessHTTP(c, 403, "disabled")
	case errors.Is(err, business.ErrDenied):
		return businessHTTP(c, 403, "forbidden")
	case errors.Is(err, business.ErrStale):
		return businessHTTP(c, 409, "revision_conflict")
	case errors.Is(err, business.ErrQuota):
		return businessHTTP(c, 429, "budget_exhausted")
	case errors.Is(err, business.ErrUnknownOutcome):
		return businessHTTP(c, 409, "unknown_outcome")
	case errors.Is(err, business.ErrUnsupported):
		return businessHTTP(c, 422, "unsupported")
	default:
		return businessHTTP(c, 503, "unavailable")
	}
}
func parseBusinessBody(c *fiber.Ctx, value any) error {
	if len(c.Body()) > 64<<10 {
		return business.ErrDenied
	}
	d := json.NewDecoder(bytes.NewReader(c.Body()))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		return business.ErrDenied
	}
	if d.Decode(new(any)) != io.EOF {
		return business.ErrDenied
	}
	return nil
}
func connectionRef(key business.Key) string {
	value, err := json.Marshal(key)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:16])
}
func modeDTO(mode business.Mode) string {
	if mode == business.ModeConfirm {
		return "confirmed_send"
	}
	if mode == "" {
		return "off"
	}
	return string(mode)
}
func connectionDTO(c business.Connection, provider string, available bool) fiber.Map {
	state := "processing_off"
	if c.Enabled {
		state = "connected"
	}
	if c.Policy.Consent && c.Policy.Enabled {
		state = modeDTO(c.Policy.Mode)
	}
	if c.Paused {
		state = "paused"
	}
	if !c.Enabled {
		state = "disconnected"
	}
	if c.Quarantined {
		state = "quarantined"
	}
	if c.NeedsRefresh {
		state = "needs_refresh"
	}
	return fiber.Map{"id": connectionRef(c.Key), "revision": c.Epoch, "account_label": "Telegram " + strconv.FormatInt(c.OwnerTelegramID, 10), "account_ref": strconv.FormatInt(c.OwnerTelegramID, 10), "state": state, "is_enabled": c.Enabled, "rights": c.Rights, "allowed_methods": c.Policy.AllowedMethods, "token_only": c.Policy.TokenOnly, "auto_reply": c.Policy.AutoReply, "bot_ref": strconv.FormatInt(c.Key.BotID, 10), "mode": modeDTO(c.Policy.Mode), "processing_consent": c.Policy.Consent, "provider_available": available, "provider_label": provider, "policy_version": secretaryConsentVersion(provider), "retention_days": 7, "requires_token_only": true, "verified_at": c.VerifiedAt, "paused": c.Paused, "budget": fiber.Map{"input_tokens": 250000, "output_tokens": 50000}}
}
func peerDTO(p business.Peer) fiber.Map {
	return fiber.Map{"id": strconv.FormatInt(p.ID, 10), "revision": p.Revision, "label": "Telegram " + strconv.FormatInt(p.ID, 10), "recipient_ref": strconv.FormatInt(p.ID, 10), "mode": modeDTO(p.Policy.Mode), "paused": !p.Policy.Enabled, "processing_consent": p.Policy.Consent, "allowed_methods": p.Policy.AllowedMethods, "token_only": p.Policy.TokenOnly, "auto_reply": p.Policy.AutoReply, "context_revision": p.Revision, "last_message_at": p.LastIncomingAt}
}
func draftRef(scope business.Scope, id string) string {
	data, err := json.Marshal([]string{connectionRef(scope.Key), strconv.FormatInt(scope.PeerID, 10), id})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}
func draftDTO(d business.Draft, a *business.Action) fiber.Map {
	state := d.State
	out := fiber.Map{"id": draftRef(d.Scope, d.ID), "revision": d.Revision, "connection_id": connectionRef(d.Scope.Key), "peer_id": strconv.FormatInt(d.Scope.PeerID, 10), "text": d.Text, "state": state, "expires_at": d.ExpiresAt, "recipient_ref": strconv.FormatInt(d.Scope.PeerID, 10), "recipient_label": "Telegram " + strconv.FormatInt(d.Scope.PeerID, 10)}
	if a != nil {
		out["digest"] = a.Digest
		out["approval_id"] = a.Digest
		out["operation_id"] = a.ID
		out["state"] = a.State
		if a.Failure != "" {
			out["reason_code"] = a.Failure
		}
	}
	return out
}
func (s *Server) businessScope(c *fiber.Ctx, ref, peer string) (business.Scope, *business.State, error) {
	owner := auth.GetClaims(c).UserID
	states, err := s.telegramBusiness.List(c.UserContext(), owner)
	if err != nil {
		return business.Scope{}, nil, err
	}
	for i := range states {
		if connectionRef(states[i].Connection.Key) == ref {
			var peerID int64
			if peer != "" {
				peerID, err = strconv.ParseInt(peer, 10, 64)
				if err != nil || peerID <= 0 {
					return business.Scope{}, nil, business.ErrDenied
				}
			}
			return business.Scope{Key: states[i].Connection.Key, OwnerID: owner, PeerID: peerID}, &states[i], nil
		}
	}
	return business.Scope{}, nil, business.ErrDenied
}
func (s *Server) requestBusinessScope(c *fiber.Ctx) (business.Scope, *business.State, error) {
	return s.businessScope(c, c.Params("id"), c.Params("peer"))
}
func (s *Server) businessConnections(c *fiber.Ctx) error {
	states, err := s.telegramBusiness.List(c.UserContext(), auth.GetClaims(c).UserID)
	if err != nil {
		return businessError(c, err)
	}
	for i := range states {
		st := &states[i]
		if st.Connection.NeedsRefresh {
			if reconcileErr := s.telegramBusiness.Reconcile(c.UserContext(), st.Connection.Key); reconcileErr != nil && s.logger != nil {
				s.logger.Warn("Telegram Secretary connection refresh unavailable")
			}
		}
	}
	states, err = s.telegramBusiness.List(c.UserContext(), auth.GetClaims(c).UserID)
	if err != nil {
		return businessError(c, err)
	}
	connections := make([]fiber.Map, 0, len(states))
	for i := range states {
		connections = append(connections, s.secretaryConnectionDTO(states[i].Connection))
	}
	methods := business.SupportedMethods()
	caps := make([]fiber.Map, 0, len(methods))
	for _, m := range methods {
		capability := fiber.Map{"method": m, "right": businessMethodRight(m), "supported": true, "enabled": s.telegramBusiness.LiveWritesEnabled()}
		if !s.telegramBusiness.LiveWritesEnabled() {
			capability["reason_code"] = "live_actions_disabled"
		}
		caps = append(caps, capability)
	}
	return c.JSON(fiber.Map{"connections": connections, "capabilities": caps, "live_writes_enabled": s.telegramBusiness.LiveWritesEnabled()})
}
func businessMethodRight(m business.Method) string {
	switch m {
	case business.SendText, business.SendPhoto, business.SendVoice, business.SendAudio, business.SendVideo, business.SendDocument, business.EditText:
		return "can_reply"
	case business.ReadMessage:
		return "can_read_messages"
	case business.DeleteSent:
		return "can_delete_sent_messages"
	case business.DeleteAll:
		return "can_delete_all_messages"
	case business.SetName:
		return "can_edit_name"
	case business.SetBio:
		return "can_edit_bio"
	case business.SetUsername:
		return "can_edit_username"
	case business.SetProfilePhoto, business.RemoveProfilePhoto:
		return "can_edit_profile_photo"
	case business.SetGiftSettings:
		return "can_change_gift_settings"
	case business.GetStarBalance, business.GetGifts:
		return "can_view_gifts_and_stars"
	case business.ConvertGift, business.DeleteGift:
		return "can_convert_gifts_to_stars"
	case business.UpgradeGift, business.TransferGift:
		return "can_transfer_and_upgrade_gifts"
	case business.TransferStars:
		return "can_transfer_stars"
	default:
		return "can_manage_stories"
	}
}
func (s *Server) businessPeers(c *fiber.Ctx) error {
	scope, state, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	peers, err := s.telegramBusiness.Peers(c.UserContext(), scope.OwnerID, scope.Key)
	if err != nil {
		return businessError(c, err)
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })
	out := make([]fiber.Map, 0, len(peers))
	for i := range peers {
		out = append(out, s.secretaryPeerDTO(state.Connection, peers[i]))
	}
	return c.JSON(fiber.Map{"peers": out, "drafts": []any{}})
}
func (s *Server) businessMessages(c *fiber.Ctx) error {
	scope, st, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	limit, err := strconv.Atoi(c.Query("limit", "20"))
	if err != nil || limit < 1 || limit > 20 {
		return businessHTTP(c, 400, "invalid_request")
	}
	messages, err := s.telegramBusiness.OwnerMessages(c.UserContext(), scope)
	if errors.Is(err, business.ErrDenied) {
		messages = nil
		err = nil
	}
	if err != nil {
		return businessError(c, err)
	}
	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	out := make([]fiber.Map, 0, len(messages))
	for i := range messages {
		m := &messages[i]
		out = append(out, fiber.Map{"id": strconv.Itoa(m.ID), "revision": m.Revision, "direction": m.Direction, "text": m.Text, "extracted_text": m.ExtractedText, "extracted_provider": m.ExtractedProvider, "created_at": m.Date, "deleted": m.Deleted})
	}
	drafts, err := s.telegramBusiness.Drafts(c.UserContext(), scope)
	if errors.Is(err, business.ErrDenied) {
		drafts = nil
		err = nil
	}
	if err != nil {
		return businessError(c, err)
	}
	draftOut := make([]fiber.Map, 0, len(drafts))
	for i := range drafts {
		d := &drafts[i]
		var a *business.Action
		if d.ActionID != "" {
			var actionErr error
			a, actionErr = s.telegramBusiness.Action(c.UserContext(), scope, d.ActionID)
			if actionErr != nil {
				a = nil
			}
		}
		draftOut = append(draftOut, draftDTO(*d, a))
	}
	var rev uint64
	if p := st.Peers[scope.PeerID]; p != nil {
		rev = p.Revision
	}
	contextInfo, contextErr := s.telegramBusiness.Context(c.UserContext(), scope)
	if contextErr != nil && !errors.Is(contextErr, business.ErrDenied) && !errors.Is(contextErr, business.ErrUnavailable) {
		return businessError(c, contextErr)
	}
	omitted := 0
	if peer := st.Peers[scope.PeerID]; peer != nil {
		omitted = peer.LastMediaOmitted
	}
	return c.JSON(fiber.Map{"messages": out, "drafts": draftOut, "context_revision": rev, "context": contextInfo, "media_omitted": omitted})
}

type businessMutation struct {
	ExpectedRevision *uint64             `json:"expected_revision"`
	Mode             string              `json:"mode"`
	Consent          bool                `json:"processing_consent"`
	PolicyVersion    string              `json:"policy_version"`
	Paused           bool                `json:"paused"`
	AllowedMethods   []business.Method   `json:"allowed_methods"`
	TokenOnly        bool                `json:"token_only"`
	AutoReply        bool                `json:"auto_reply"`
	MediaConsent     bool                `json:"media_consent"`
	MediaProvider    string              `json:"media_provider"`
	AutoRules        *business.AutoRules `json:"auto_rules"`
}

func scopeRevision(scope business.Scope, st *business.State) uint64 {
	if scope.PeerID == 0 {
		return st.Connection.Epoch
	}
	if p := st.Peers[scope.PeerID]; p != nil {
		return p.Revision
	}
	return 0
}
func (s *Server) businessPolicy(c *fiber.Ctx) error {
	scope, st, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	var body businessMutation
	if parseBusinessBody(c, &body) != nil || body.ExpectedRevision == nil {
		return businessHTTP(c, 400, "invalid_request")
	}
	mode := business.Mode(body.Mode)
	if body.Mode == "confirmed_send" {
		mode = business.ModeConfirm
	}
	if mode != business.ModeOff && mode != business.ModeDraft && mode != business.ModeConfirm && mode != business.ModeAuto {
		return businessHTTP(c, 400, "invalid_request")
	}
	provider, _ := s.secretaryProfile()
	if body.Consent && (body.PolicyVersion != secretaryConsentVersion(provider) || provider == "") {
		return businessHTTP(c, 409, "revision_conflict")
	}
	policy := business.PeerPolicy{Enabled: mode != business.ModeOff, Consent: body.Consent, Mode: mode, Provider: provider, TokenOnly: body.TokenOnly, AutoReply: body.AutoReply}
	if policy.Enabled && !s.businessEncryptionEnabled {
		return businessError(c, business.ErrUnavailable)
	}
	descriptor := ""
	if s.businessSources != nil {
		descriptor = s.businessSources.Descriptor(scope)
	}
	if body.MediaConsent && (descriptor == "" || body.MediaProvider != descriptor) {
		return businessError(c, business.ErrStale)
	}
	policy.MediaConsent = body.MediaConsent
	policy.MediaProvider = descriptor
	policy.AutoRules = body.AutoRules

	if body.AllowedMethods != nil {
		policy.AllowedMethods = body.AllowedMethods
	} else if scope.PeerID == 0 {
		policy.AllowedMethods = st.Connection.Policy.AllowedMethods
	} else if peer := st.Peers[scope.PeerID]; peer != nil {
		policy.AllowedMethods = peer.Policy.AllowedMethods
	}
	if (mode == business.ModeConfirm || mode == business.ModeAuto) && len(policy.AllowedMethods) == 0 {
		policy.AllowedMethods = []business.Method{business.SendText}
	}
	if mode == business.ModeAuto && !body.AutoReply {
		return businessHTTP(c, 400, "invalid_request")
	}
	if mode != business.ModeAuto {
		policy.AutoReply = false
	}
	if mode == business.ModeOff {
		policy.AllowedMethods = nil
	}
	if err := s.telegramBusiness.Configure(c.UserContext(), business.ConfigureInput{Scope: scope, Policy: policy, ExpectedRevision: *body.ExpectedRevision}); err != nil {
		return businessError(c, err)
	}
	return s.businessScopeResponse(c, scope)
}
func (s *Server) businessScopeResponse(c *fiber.Ctx, scope business.Scope) error {
	st, err := s.telegramBusiness.GetState(c.UserContext(), scope.OwnerID, scope.Key)
	if err != nil {
		return businessError(c, err)
	}
	if scope.PeerID == 0 {
		return c.JSON(fiber.Map{"connection": s.secretaryConnectionDTO(st.Connection)})
	}
	p := st.Peers[scope.PeerID]
	if p == nil {
		return businessHTTP(c, 404, "not_found")
	}
	return c.JSON(fiber.Map{"peer": s.secretaryPeerDTO(st.Connection, *p)})
}
func (s *Server) businessPause(c *fiber.Ctx) error {
	scope, _, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	var body businessMutation
	if parseBusinessBody(c, &body) != nil || body.ExpectedRevision == nil {
		return businessHTTP(c, 400, "invalid_request")
	}
	if err := s.telegramBusiness.Pause(c.UserContext(), scope, *body.ExpectedRevision, body.Paused); err != nil {
		return businessError(c, err)
	}
	return s.businessScopeResponse(c, scope)
}
func (s *Server) businessPurge(c *fiber.Ctx) error {
	scope, st, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	var body businessMutation
	if parseBusinessBody(c, &body) != nil || body.ExpectedRevision == nil {
		return businessHTTP(c, 400, "invalid_request")
	}
	if *body.ExpectedRevision != scopeRevision(scope, st) {
		return businessError(c, business.ErrStale)
	}
	if err := s.telegramBusiness.PurgeRevision(c.UserContext(), scope, *body.ExpectedRevision); err != nil {
		return businessError(c, err)
	}
	if s.businessMedia != nil {
		if err := s.businessMedia.PurgeScope(c.UserContext(), scope); err != nil {
			return businessError(c, business.ErrUnavailable)
		}
	}
	return c.JSON(fiber.Map{"purged": true})
}
func (s *Server) businessRebind(c *fiber.Ctx) error {
	scope, _, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	var body businessMutation
	if parseBusinessBody(c, &body) != nil || body.ExpectedRevision == nil {
		return businessHTTP(c, 400, "invalid_request")
	}
	if err := s.telegramBusiness.Rebind(c.UserContext(), scope.OwnerID, scope.Key, *body.ExpectedRevision); err != nil {
		return businessError(c, err)
	}
	return s.businessScopeResponse(c, scope)
}
func (s *Server) businessExport(c *fiber.Ctx) error {
	scope, _, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	data, err := s.telegramBusiness.Export(c.UserContext(), scope)
	if err != nil {
		return businessError(c, err)
	}
	if len(data) > 10<<20 {
		return businessHTTP(c, 413, "invalid_request")
	}
	c.Set("Content-Disposition", `attachment; filename="telegram-business.json"`)
	c.Type("json")
	return c.Send(data)
}
func (s *Server) businessCreateDraft(c *fiber.Ctx) error {
	scope, st, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	var body struct {
		ExpectedRevision *uint64 `json:"expected_revision"`
		Text             string  `json:"text"`
		Generate         bool    `json:"generate"`
	}
	if parseBusinessBody(c, &body) != nil || body.ExpectedRevision == nil {
		return businessHTTP(c, 400, "invalid_request")
	}
	if *body.ExpectedRevision != scopeRevision(scope, st) {
		return businessError(c, business.ErrStale)
	}
	var draft *business.Draft
	if body.Generate {
		draft, err = s.telegramBusiness.DraftRevision(c.UserContext(), scope, *body.ExpectedRevision)
	} else {
		draft, err = s.telegramBusiness.CreateDraftRevision(c.UserContext(), scope, body.Text, *body.ExpectedRevision)
	}
	if err != nil {
		return businessError(c, err)
	}
	return c.JSON(fiber.Map{"draft": draftDTO(*draft, nil)})
}
func (s *Server) requestBusinessDraft(c *fiber.Ctx) (business.Scope, *business.Draft, error) {
	data, err := base64.RawURLEncoding.DecodeString(c.Params("draft"))
	if err != nil || len(data) > 1024 {
		return business.Scope{}, nil, business.ErrDenied
	}
	var parts []string
	if json.Unmarshal(data, &parts) != nil || len(parts) != 3 || parts[2] == "" {
		return business.Scope{}, nil, business.ErrDenied
	}
	scope, _, err := s.businessScope(c, parts[0], parts[1])
	if err != nil {
		return business.Scope{}, nil, err
	}
	drafts, err := s.telegramBusiness.Drafts(c.UserContext(), scope)
	if err != nil {
		return scope, nil, err
	}
	for i := range drafts {
		d := &drafts[i]
		if d.ID == parts[2] {
			return scope, d, nil
		}
	}
	return scope, nil, business.ErrUnavailable
}
func (s *Server) businessEditDraft(c *fiber.Ctx) error {
	scope, d, err := s.requestBusinessDraft(c)
	if err != nil {
		return businessError(c, err)
	}
	var body struct {
		ExpectedRevision *uint64 `json:"expected_revision"`
		Text             string  `json:"text"`
	}
	if parseBusinessBody(c, &body) != nil || body.ExpectedRevision == nil {
		return businessHTTP(c, 400, "invalid_request")
	}
	d, err = s.telegramBusiness.Edit(c.UserContext(), scope, d.ID, *body.ExpectedRevision, body.Text)
	if err != nil {
		return businessError(c, err)
	}
	return c.JSON(fiber.Map{"draft": draftDTO(*d, nil)})
}
func (s *Server) businessApproveDraft(c *fiber.Ctx) error {
	scope, d, err := s.requestBusinessDraft(c)
	if err != nil {
		return businessError(c, err)
	}
	if !s.businessReplyWindow(c, scope) {
		return businessHTTP(c, 409, "reply_window_closed")
	}
	var body struct {
		ExpectedRevision *uint64 `json:"expected_revision"`
		Digest           string  `json:"expected_digest"`
	}
	if parseBusinessBody(c, &body) != nil || body.ExpectedRevision == nil {
		return businessHTTP(c, 400, "invalid_request")
	}
	if *body.ExpectedRevision != d.Revision {
		return businessError(c, business.ErrStale)
	}
	var a *business.Action
	if d.ActionID != "" {
		a, err = s.telegramBusiness.Action(c.UserContext(), scope, d.ActionID)
	} else {
		text, marshalErr := json.Marshal(d.Text)
		if marshalErr != nil {
			return businessError(c, business.ErrUnavailable)
		}
		a, err = s.telegramBusiness.Prepare(c.UserContext(), business.PrepareInput{Scope: scope, Method: business.SendText, Params: business.Params{"text": text}, DraftID: d.ID})
	}
	if err != nil {
		return businessError(c, err)
	}
	if body.Digest != "" && body.Digest != a.Digest {
		return businessError(c, business.ErrStale)
	}
	a, err = s.telegramBusiness.Approve(c.UserContext(), business.ApprovalInput{Scope: scope, ActionID: a.ID, Digest: a.Digest, Step: 1})
	if err != nil {
		return businessError(c, err)
	}
	d.ActionID = a.ID
	return c.JSON(fiber.Map{"draft": draftDTO(*d, a)})
}
func (s *Server) businessExecuteDraft(c *fiber.Ctx) error {
	scope, d, err := s.requestBusinessDraft(c)
	if err != nil {
		return businessError(c, err)
	}
	if !s.businessReplyWindow(c, scope) {
		return businessHTTP(c, 409, "reply_window_closed")
	}
	var body struct {
		ExpectedRevision *uint64 `json:"expected_revision"`
		ApprovalID       string  `json:"approval_id"`
	}
	if parseBusinessBody(c, &body) != nil || body.ExpectedRevision == nil {
		return businessHTTP(c, 400, "invalid_request")
	}
	if *body.ExpectedRevision != d.Revision || d.ActionID == "" {
		return businessError(c, business.ErrStale)
	}
	a, err := s.telegramBusiness.Action(c.UserContext(), scope, d.ActionID)
	if err != nil {
		return businessError(c, err)
	}
	if body.ApprovalID == "" || body.ApprovalID != a.Digest {
		return businessError(c, business.ErrStale)
	}
	a, err = s.telegramBusiness.Execute(c.UserContext(), scope, a.ID)
	if err != nil {
		return businessError(c, err)
	}
	return c.JSON(fiber.Map{"draft": draftDTO(*d, a)})
}
func (s *Server) businessPrepareAction(c *fiber.Ctx) error {
	scope, st, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	var body struct {
		ExpectedRevision *uint64         `json:"expected_revision"`
		Method           business.Method `json:"method"`
		Params           business.Params `json:"params"`
	}
	if parseBusinessBody(c, &body) != nil || body.ExpectedRevision == nil {
		return businessHTTP(c, 400, "invalid_request")
	}
	if *body.ExpectedRevision != scopeRevision(scope, st) {
		return businessError(c, business.ErrStale)
	}
	a, err := s.telegramBusiness.Prepare(c.UserContext(), business.PrepareInput{Scope: scope, Method: body.Method, Params: body.Params, ExpectedRevision: body.ExpectedRevision})
	if err != nil {
		return businessError(c, err)
	}
	return c.JSON(fiber.Map{"action": actionDTO(a)})
}
func (s *Server) businessGetAction(c *fiber.Ctx) error {
	scope, _, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	a, err := s.telegramBusiness.Action(c.UserContext(), scope, c.Params("action"))
	if err != nil {
		return businessError(c, err)
	}
	return c.JSON(fiber.Map{"action": actionDTO(a)})
}
func (s *Server) businessApproveAction(c *fiber.Ctx) error {
	scope, _, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	var body struct {
		Digest string `json:"digest"`
		Step   int    `json:"step"`
	}
	if parseBusinessBody(c, &body) != nil || strings.TrimSpace(body.Digest) == "" {
		return businessHTTP(c, 400, "invalid_request")
	}
	a, err := s.telegramBusiness.Approve(c.UserContext(), business.ApprovalInput{Scope: scope, ActionID: c.Params("action"), Digest: body.Digest, Step: body.Step})
	if err != nil {
		return businessError(c, err)
	}
	return c.JSON(fiber.Map{"action": actionDTO(a)})
}
func (s *Server) businessExecuteAction(c *fiber.Ctx) error {
	scope, _, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	var body struct {
		Digest string `json:"digest"`
	}
	if parseBusinessBody(c, &body) != nil {
		return businessHTTP(c, 400, "invalid_request")
	}
	a, err := s.telegramBusiness.Action(c.UserContext(), scope, c.Params("action"))
	if err != nil {
		return businessError(c, err)
	}
	if a.Digest != body.Digest {
		return businessError(c, business.ErrStale)
	}
	a, err = s.telegramBusiness.Execute(c.UserContext(), scope, a.ID)
	if err != nil {
		return businessError(c, err)
	}
	return c.JSON(fiber.Map{"action": actionDTO(a)})
}

func (s *Server) businessListActions(c *fiber.Ctx) error {
	scope, _, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	actions, err := s.telegramBusiness.Actions(c.UserContext(), scope)
	if err != nil {
		return businessError(c, err)
	}
	out := make([]fiber.Map, 0, len(actions))
	for i := range actions {
		out = append(out, actionDTO(&actions[i]))
	}
	return c.JSON(fiber.Map{"actions": out})
}
func (s *Server) businessUpload(c *fiber.Ctx) error {
	scope, st, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	if s.businessMedia == nil || st.Connection.Quarantined || st.Connection.Paused || st.Connection.NeedsRefresh || !st.Connection.Enabled {
		return businessError(c, business.ErrUnavailable)
	}
	if len(c.Body()) > 2<<20 {
		return businessHTTP(c, 413, "invalid_request")
	}
	var body struct {
		Filename string `json:"filename"`
		MIME     string `json:"mime_type"`
		Data     []byte `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF {
		return businessHTTP(c, 400, "invalid_request")
	}
	ref, err := s.businessMedia.Put(c.UserContext(), scope, body.Filename, body.MIME, body.Data, business.MediaAuthorityFor(st.Connection, st.Peers[scope.PeerID]))
	if err != nil {
		return businessError(c, err)
	}
	current, checkErr := s.telegramBusiness.GetState(c.UserContext(), scope.OwnerID, scope.Key)
	if checkErr != nil || current.Connection.Quarantined || current.Connection.Paused || current.Connection.NeedsRefresh || !current.Connection.Enabled || business.MediaAuthorityFor(current.Connection, current.Peers[scope.PeerID]) != business.MediaAuthorityFor(st.Connection, st.Peers[scope.PeerID]) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(c.UserContext()), 3*time.Second)
		defer cancel()
		if cleanupErr := s.businessMedia.Delete(cleanup, scope, ref); cleanupErr != nil && s.logger != nil {
			s.logger.Warn("Telegram Secretary stale upload cleanup failed")
		}
		return businessError(c, business.ErrStale)
	}
	return c.JSON(fiber.Map{"media_ref": ref})
}

func (s *Server) businessCompact(c *fiber.Ctx) error {
	scope, _, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	var body struct {
		ExpectedRevision *uint64 `json:"expected_revision"`
	}
	if parseBusinessBody(c, &body) != nil || body.ExpectedRevision == nil {
		return businessHTTP(c, 400, "invalid_request")
	}
	result, err := s.telegramBusiness.CompactRevision(c.UserContext(), scope, *body.ExpectedRevision)
	if err != nil {
		return businessError(c, err)
	}
	return c.JSON(fiber.Map{"context": result})
}

func (s *Server) businessCancelAction(c *fiber.Ctx) error {
	scope, _, err := s.requestBusinessScope(c)
	if err != nil {
		return businessError(c, err)
	}
	var body struct {
		Digest string `json:"digest"`
	}
	if parseBusinessBody(c, &body) != nil || body.Digest == "" {
		return businessHTTP(c, 400, "invalid_request")
	}
	if cancelErr := s.telegramBusiness.CancelAction(c.UserContext(), scope, c.Params("action"), body.Digest); cancelErr != nil {
		return businessError(c, cancelErr)
	}
	a, err := s.telegramBusiness.Action(c.UserContext(), scope, c.Params("action"))
	if err != nil {
		return businessError(c, err)
	}
	return c.JSON(fiber.Map{"action": actionDTO(a)})
}
func (s *Server) businessRejectDraft(c *fiber.Ctx) error {
	scope, draft, err := s.requestBusinessDraft(c)
	if err != nil {
		return businessError(c, err)
	}
	var body struct {
		Revision *uint64 `json:"expected_revision"`
	}
	if parseBusinessBody(c, &body) != nil || body.Revision == nil {
		return businessHTTP(c, 400, "invalid_request")
	}
	if err := s.telegramBusiness.RejectDraft(c.UserContext(), scope, draft.ID, *body.Revision); err != nil {
		return businessError(c, err)
	}
	return c.JSON(fiber.Map{"rejected": true})
}

func (s *Server) businessReplyWindow(c *fiber.Ctx, scope business.Scope) bool {
	if scope.PeerID == 0 {
		return true
	}
	st, err := s.telegramBusiness.GetState(c.UserContext(), scope.OwnerID, scope.Key)
	if err != nil {
		return false
	}
	peer := st.Peers[scope.PeerID]
	return peer != nil && !peer.LastIncomingAt.IsZero() && time.Now().UTC().Before(peer.LastIncomingAt.Add(24*time.Hour-30*time.Second))
}
func actionDTO(action *business.Action) fiber.Map {
	if action == nil {
		return nil
	}
	return fiber.Map{"id": action.ID, "method": action.Method, "params": action.Params, "digest": action.Digest, "state": action.State, "required_steps": action.RequiredSteps, "approval_steps": action.ApprovalSteps, "expires_at": action.ExpiresAt, "created_at": action.CreatedAt, "receipt": action.Receipt, "connection_id": connectionRef(action.Scope.Key), "peer_id": strconv.FormatInt(action.Scope.PeerID, 10), "automatic": action.Automatic}
}

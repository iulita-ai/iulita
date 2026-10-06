package modelruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/models"
)

func validateProbeKey(key string, now time.Time) error {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 || len(parts[1]) < 16 || len(parts[1]) > 80 {
		return failure("invalid_idempotency_key", "Use a timestamped random idempotency key", 422)
	}
	millis, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return failure("invalid_idempotency_key", "Use a timestamped random idempotency key", 422)
	}
	created := time.UnixMilli(millis)
	if now.Sub(created) > StageTTL || created.After(now.Add(30*time.Second)) {
		return failure("idempotency_key_expired", "Start a new explicit check with a fresh key", 410)
	}
	return nil
}

// Probe starts an actor-authorized synthetic check with a durable marker.
func (m *Manager) Probe(ctx context.Context, actor string, req ProbeRequest) (ProbeView, error) {
	if err := m.authorizeActor(ctx, actor); err != nil {
		return ProbeView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ProbeView{}, failure("runtime_stopped", "Model runtime is stopping", 503)
	}
	now := m.now().UTC()
	if err := validateProbeKey(req.IdempotencyKey, now); err != nil {
		return ProbeView{}, err
	}
	requestHash := hashJSON(req)
	// Markers are checked before quota/CAS so a lost response returns the same
	// operation even after its work has completed or the stage was activated.
	for id := range m.state.Probes {
		r := m.state.Probes[id]
		if r.Actor == actor && r.Key == req.IdempotencyKey {
			if r.RequestHash != requestHash {
				return ProbeView{}, failure("idempotency_conflict", "This key belongs to a different check", 409)
			}
			return copyProbeView(r.View), nil
		}
	}
	if err := m.checkRevisionLocked(req.ExpectedRevision); err != nil {
		return ProbeView{}, err
	}
	if req.Kind != "text" && req.Kind != "vision" && req.Kind != "tools" {
		return ProbeView{}, failure("invalid_probe_kind", "Use a text, image or tool check", 422)
	}
	settings := m.state.Settings
	connections := m.state.Connections
	if req.StageID != "" {
		s, err := m.stageLocked(actor, req.StageID)
		if err != nil {
			return ProbeView{}, err
		}
		settings = s.Settings
		connections = s.Connections
	}
	var profile models.Profile
	found := false
	for _, p := range settings.Profiles {
		if p.ID == req.ProfileID {
			profile = p
			found = true
			break
		}
	}
	if !found {
		return ProbeView{}, failure("invalid_reference", "Profile does not exist", 422)
	}
	d, known := models.Lookup(profile.Connection, profile.Model)
	if req.Kind == "vision" && profile.Connection != "claude" && (!known || !d.Images) {
		return ProbeView{}, failure("unsupported_images", "This documented model cannot accept images", 422)
	}
	c, ok := connections[profile.Connection]
	if !ok || !c.Connection.Enabled {
		return ProbeView{}, failure("credential_required", "Configure a current connection before checking it", 422)
	}
	if _, denied := m.state.Denied[c.Connection.Generation]; denied {
		return ProbeView{}, failure("credential_revoked", "The credential has been revoked", 403)
	}
	for _, provider := range m.state.Settings.Policy.ForbiddenProviders {
		if provider == profile.Connection {
			return ProbeView{}, failure("forbidden_provider", "This model provider is forbidden", 403)
		}
	}
	if len(m.running) >= 2 || m.running[profile.Connection] != "" {
		return ProbeView{}, failure("probe_busy", "At most two checks and one check per connection can run at once", 429)
	}
	var starts []time.Time
	// Durable starts preserve the per-actor rate window across restart.
	for id := range m.state.Probes {
		record := m.state.Probes[id]
		if record.Actor == actor && now.Sub(record.View.StartedAt) < time.Minute {
			starts = append(starts, record.View.StartedAt)
		}
	}
	if len(starts) >= 5 {
		return ProbeView{}, failure("probe_rate_limit", "At most five checks per administrator per minute are allowed", 429)
	}
	candidate := cloneState(m.state)
	for id := range candidate.Probes {
		r := candidate.Probes[id]
		if !now.Before(r.ExpiresAt) {
			delete(candidate.Probes, id)
		}
	}
	if len(candidate.Probes) >= maxProbeRecords {
		return ProbeView{}, failure("probe_record_limit", "Recent checks fill the retention window; wait before starting another", 429)
	}
	// A small, fixed output cap bounds each probe independently from production
	// settings. Reasoning is not disabled or silently changed for the check.
	probeProfile := profile
	if probeProfile.MaxOutputTokens > 2048 {
		probeProfile.MaxOutputTokens = 2048
	}
	inner, err := m.factory(probeProfile, c.handle())
	if err != nil || inner == nil {
		return ProbeView{}, failure("client_build_failed", "Could not construct a test client", 422)
	}
	view := ProbeView{ID: randomID(), ProfileID: req.ProfileID, StageID: req.StageID, Kind: req.Kind, Status: "running", StartedAt: now, Deadline: now.Add(ProbeDeadline)}
	record := probeRecord{Revision: req.ExpectedRevision, View: view, Actor: actor, Key: req.IdempotencyKey, RequestHash: requestHash, Fingerprint: profileFingerprint(profile, c.Connection), Generation: c.Connection.Generation, Provider: profile.Connection, ExpiresAt: now.Add(StageTTL)}
	candidate.Probes[view.ID] = record
	if err := m.persistLocked(ctx, candidate, actor); err != nil {
		return ProbeView{}, err
	}
	m.state = candidate
	m.running[profile.Connection] = view.ID

	probeCtx, cancel := context.WithTimeout(context.Background(), ProbeDeadline)
	m.probeCancel[view.ID] = cancel
	guarded := &guardedProvider{manager: m, inner: inner, provider: profile.Connection, generation: c.Connection.Generation, stageID: req.StageID}
	m.activity.Add(1)
	go m.runProbe(probeCtx, record, profile, guarded) //nolint:gosec // G118: durable checks outlive the HTTP request; their own deadline, cancellation handle and shutdown guard bound their lifetime.
	return copyProbeView(view), nil
}
func copyProbeView(v ProbeView) ProbeView {
	if v.Result != nil {
		e := *v.Result
		v.Result = &e
	}
	return v
}

// ProbeStatus returns the actor-owned check without rerunning provider calls.
func (m *Manager) ProbeStatus(ctx context.Context, actor, id string) (ProbeView, error) {
	if err := m.authorizeActor(ctx, actor); err != nil {
		return ProbeView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.state.Probes[id]
	if !ok || r.Actor != actor {
		return ProbeView{}, failure("probe_not_found", "The check does not exist or is unavailable", 404)
	}
	return copyProbeView(r.View), nil
}

// CancelProbe persists cancellation intent before canceling admitted work.
func (m *Manager) CancelProbe(ctx context.Context, actor, id string) (ProbeView, error) {
	if err := m.authorizeActor(ctx, actor); err != nil {
		return ProbeView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.state.Probes[id]
	if !ok || r.Actor != actor {
		return ProbeView{}, failure("probe_not_found", "The check does not exist or is unavailable", 404)
	}
	if r.View.Status != "running" && r.View.Status != "cancel_requested" {
		return copyProbeView(r.View), nil
	}
	candidate := cloneState(m.state)
	r.View.Status = "cancel_requested"
	candidate.Probes[id] = r
	if err := m.persistLocked(ctx, candidate, actor); err != nil {
		return ProbeView{}, err
	}
	m.state = candidate
	if cancel := m.probeCancel[id]; cancel != nil {
		cancel()
	}
	return copyProbeView(r.View), nil
}
func (m *Manager) runProbe(ctx context.Context, r probeRecord, profile models.Profile, provider llm.Provider) {
	defer m.activity.Done()
	usage := llm.Usage{}
	served := ""
	call := func(req llm.Request) (llm.Response, error) {
		if err := m.authorizeActor(ctx, r.Actor); err != nil {
			return llm.Response{}, err
		}
		req.UserID = r.Actor
		req.Operation = "probe"
		req.ProfileID = r.View.ProfileID
		req.PolicyRevision = r.Revision
		req.Role = "everyday"
		if r.View.Kind == "vision" {
			req.Role = "vision"
		}
		response, err := provider.Complete(ctx, req)
		usage.InputTokens += response.Usage.InputTokens
		usage.OutputTokens += response.Usage.OutputTokens
		usage.CacheReadInputTokens += response.Usage.CacheReadInputTokens
		usage.CacheCreationInputTokens += response.Usage.CacheCreationInputTokens
		if err != nil {
			return response, err
		}
		terminalText := response.FinishReason == "stop" || response.FinishReason == "end_turn"
		terminalTool := response.FinishReason == "tool_calls" || response.FinishReason == "tool_use"
		if len(response.ToolCalls) > 0 && !terminalTool || len(response.ToolCalls) == 0 && !terminalText {
			return response, failure("incomplete_probe_response", "The provider did not report a complete response", 422)
		}
		if !response.ModelVerified || !modelIdentityMatches(profile, response.Model) {
			return response, failure("unverified_model_identity", "The response did not establish the expected served model", 422)
		}
		if served != "" && served != response.Model {
			return response, failure("model_identity_changed", "The served model changed within the check", 422)
		}
		served = response.Model
		return response, nil
	}
	passed := false
	errorCode := "probe_failed"
	switch r.View.Kind {
	case "text":
		nonce := randomID()[:12]
		response, err := call(llm.Request{SystemPrompt: "Return exactly the requested JSON object. Do not add prose or markdown.", Message: "Return {\"nonce\":\"" + nonce + "\"}. This is a synthetic connection check."})
		if err == nil && matchesNonce(response.Content, nonce) {
			passed = true
		} else if err != nil {
			errorCode = probeErrorCode(err)
		}
	case "vision":
		nonce := randomID()[:8]
		image1 := imageFixture(nonce, color.RGBA{R: 255, A: 255})
		response, err := call(llm.Request{SystemPrompt: "Read the attached synthetic image. Return only JSON {\"nonce\":\"the printed label\",\"color\":\"the rectangle color\"}. Use the image, not assumptions.", Images: []llm.ImageAttachment{{Data: image1, MediaType: "image/png"}}})
		var one struct {
			Nonce string `json:"nonce"`
			Color string `json:"color"`
		}
		if err == nil && parseObject(response.Content, &one) && one.Nonce == nonce && strings.EqualFold(one.Color, "red") {
			second := randomID()[:8]
			image2 := imageFixture(second, color.RGBA{B: 255, A: 255})
			response, err = call(llm.Request{SystemPrompt: "Read both attached synthetic images in order. Return only JSON {\"nonces\":[\"first printed label\",\"second printed label\"],\"colors\":[\"first rectangle color\",\"second rectangle color\"]}.", Images: []llm.ImageAttachment{{Data: image1, MediaType: "image/png"}, {Data: image2, MediaType: "image/png"}}})
			var two struct {
				Nonces []string `json:"nonces"`
				Colors []string `json:"colors"`
			}
			passed = err == nil && parseObject(response.Content, &two) && len(two.Nonces) == 2 && len(two.Colors) == 2 && two.Nonces[0] == nonce && two.Nonces[1] == second && strings.EqualFold(two.Colors[0], "red") && strings.EqualFold(two.Colors[1], "blue")
		}
		if err != nil {
			errorCode = probeErrorCode(err)
		}
	case "tools":
		nonce := randomID()[:12]
		tool := llm.ToolDefinition{Name: "probe_echo", Description: "A pure synthetic echo, with no external actions.", InputSchema: json.RawMessage(`{"type":"object","properties":{"nonce":{"type":"string"}},"required":["nonce"],"additionalProperties":false}`)}
		request := llm.Request{SystemPrompt: "Use probe_echo with the provided nonce. After receiving its result, return only JSON containing that nonce. Do not invent tool results.", Message: "Call probe_echo with nonce " + nonce, Tools: []llm.ToolDefinition{tool}}
		response, err := call(request)
		if err == nil && len(response.ToolCalls) == 1 && response.ToolCalls[0].Name == "probe_echo" && response.ToolCalls[0].ID != "" && matchesNonce(string(response.ToolCalls[0].Input), nonce) {
			// The nonce is hexadecimal, so this fixed JSON needs no escaping.
			result := `{"nonce":"` + nonce + `"}`
			request.ToolExchanges = []llm.ToolExchange{{AssistantText: response.Content, ReasoningContent: response.ReasoningContent, ToolCalls: response.ToolCalls, Results: []llm.ToolResult{{ToolCallID: response.ToolCalls[0].ID, Content: result}}}}
			response, err = call(request)
			passed = err == nil && len(response.ToolCalls) == 0 && matchesNonce(response.Content, nonce)
		}
		if err != nil {
			errorCode = probeErrorCode(err)
		}
	}
	if ctx.Err() != nil {
		passed = false
		errorCode = "cancelled_or_deadline_unknown" //nolint:misspell // Preserve the persisted API error category.
	}
	result := Evidence{Kind: r.View.Kind, Passed: passed, CheckedAt: m.now().UTC(), RequestedModel: profile.Model, ServedModel: served, Fingerprint: r.Fingerprint, FixtureVersion: FixtureVersion, CatalogVersion: models.CatalogVersion, CompatibilityVersion: models.CompatibilityVersion(profile.Connection, profile.Model), Usage: usage}
	if r.View.Kind == "vision" {
		result.FixtureVersion = VisionFixtureVersion
	}
	if !passed {
		result.ErrorCode = errorCode
	}
	// Reauthorize before publishing evidence. A late result after password/admin
	// changes, expiry, discard or revoke cannot promote a candidate.
	authErr := m.authorizeActor(context.Background(), r.Actor)
	m.mu.Lock()
	defer m.mu.Unlock()
	canceled := ctx.Err() != nil
	delete(m.running, r.Provider)
	if cancel := m.probeCancel[r.View.ID]; cancel != nil {
		cancel()
	}
	delete(m.probeCancel, r.View.ID)
	if m.closed {
		return
	}
	current, exists := m.state.Probes[r.View.ID]
	if !exists {
		return
	}
	valid := authErr == nil && !canceled && m.now().Before(r.View.Deadline)
	if _, denied := m.state.Denied[r.Generation]; denied {
		valid = false
	}
	for _, provider := range m.state.Settings.Policy.ForbiddenProviders {
		if provider == r.Provider {
			valid = false
		}
	}
	candidate := cloneState(m.state)
	if r.View.StageID != "" {
		s := candidate.Stage
		if s == nil || s.ID != r.View.StageID || s.Actor != r.Actor || s.BaseRevision != candidate.Revision || !m.now().Before(s.ExpiresAt) || s.Connections[r.Provider].Connection.Generation != r.Generation {
			valid = false
		}
	} else {
		c := candidate.Connections[r.Provider]
		if !c.Connection.Enabled || c.Connection.Generation != r.Generation {
			valid = false
		}
	}
	if current.View.Status == "cancel_requested" {
		valid = false
	}
	if !valid {
		result.Passed = false
		result.ErrorCode = "stale_or_cancelled_check" //nolint:misspell // Preserve the persisted API error category.
	}
	current.View.Result = &result
	switch {
	case result.Passed:
		current.View.Status = "completed"
	case canceled || current.View.Status == "cancel_requested":
		current.View.Status = "cancelled" //nolint:misspell // Preserve the persisted API status spelling.
	default:
		current.View.Status = "failed"
	}
	current.View.ErrorCode = result.ErrorCode
	candidate.Probes[r.View.ID] = current
	if result.Passed {
		evidence := candidate.Evidence
		if r.View.StageID != "" {
			evidence = candidate.Stage.Evidence
		}
		if evidence[r.View.ProfileID] == nil {
			evidence[r.View.ProfileID] = map[string]Evidence{}
		}
		evidence[r.View.ProfileID][r.View.Kind] = result
	}
	if err := m.persistLocked(context.Background(), candidate, r.Actor); err != nil {
		// The durable marker remains running and will become interrupted_unknown on
		// restart. Never expose a passed badge when evidence could not be persisted.
		current.View.Status = "interrupted_unknown"
		current.View.ErrorCode = "evidence_storage_failed"
		current.View.Result = nil
		m.state.Probes[r.View.ID] = current
		return
	}
	m.state = candidate
}
func probeErrorCode(err error) string {
	var provider interface{ ModelErrorCode() string }
	if errors.As(err, &provider) {
		switch code := provider.ModelErrorCode(); code {
		case "insufficient_balance", "authentication_failed", "model_access_denied", "credential_product_mismatch", "quota_exhausted":
			return code
		}
	}
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return "provider_check_failed"
}
func modelIdentityMatches(p models.Profile, served string) bool {
	if served == p.Model {
		return true
	}
	for _, known := range models.Catalog() {
		if known.Provider == p.Connection && known.Model != p.Model && (served == known.Model || strings.HasPrefix(served, known.Model+"-")) {
			return false
		}
	}
	if strings.HasPrefix(served, p.Model+"-") {
		return true
	}
	return p.Connection == "deepseek" && p.Model == "deepseek-flash" && (served == "deepseek-v4.1-flash" || strings.HasPrefix(served, "deepseek-v4.1-flash-"))
}
func parseObject(content string, out any) bool {
	if len(content) > 4096 {
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(content)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}
func matchesNonce(content, nonce string) bool {
	var output struct {
		Nonce string `json:"nonce"`
	}
	return parseObject(content, &output) && output.Nonce == nonce
}
func imageFixtureCanvas(nonce string, rectangle color.RGBA) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, 300, 100))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(10, 10, 90, 90), image.NewUniform(rectangle), image.Point{}, draw.Src)
	drawer := font.Drawer{Dst: canvas, Src: image.NewUniform(color.Black), Face: basicfont.Face7x13, Dot: fixed.P(110, 52)}
	drawer.DrawString(nonce)
	return canvas
}

func imageFixture(nonce string, rectangle color.RGBA) []byte {
	canvas := imageFixtureCanvas(nonce, rectangle)
	// Small bitmap labels are ambiguous after provider image preprocessing.
	// Use a legible raster fixture while retaining the exact, image-only oracle.
	const scale = 4
	large := image.NewRGBA(image.Rect(0, 0, canvas.Bounds().Dx()*scale, canvas.Bounds().Dy()*scale))
	for y := 0; y < large.Bounds().Dy(); y++ {
		for x := 0; x < large.Bounds().Dx(); x++ {
			from := canvas.PixOffset(x/scale, y/scale)
			to := large.PixOffset(x, y)
			copy(large.Pix[to:to+4], canvas.Pix[from:from+4])
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, large); err != nil {
		panic("synthetic PNG fixture could not be encoded")
	}
	return buffer.Bytes()
}

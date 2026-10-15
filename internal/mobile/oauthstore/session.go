package oauthstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/ory/fosite"
	fjwt "github.com/ory/fosite/token/jwt"
)

// Binding binds stored state to its owner and authorization context.
type Binding struct {
	OwnerID, DeviceID, FamilyID, ClientID, Kid, KeyThumbprint string
	Stage                                                     Stage
	OwnerEpoch, DeviceEpoch, SigningEpoch, Generation         uint64
	AuthTime                                                  int64
}

// Session is a typed allowlist. No arbitrary Extra claims, request form or raw
// installation challenge is persisted through a framework session object.
type Session struct {
	Binding  Binding
	Expires  map[fosite.TokenType]time.Time
	JWTID    string
	IssuedAt time.Time
	claims   *fjwt.JWTClaims
}

// SetExpiresAt implements the Fosite SetExpiresAt session contract.
func (s *Session) SetExpiresAt(k fosite.TokenType, v time.Time) {
	if s.Expires == nil {
		s.Expires = map[fosite.TokenType]time.Time{}
	}
	s.Expires[k] = v.UTC()
}

// GetExpiresAt implements the Fosite GetExpiresAt session contract.
func (s *Session) GetExpiresAt(k fosite.TokenType) time.Time { return s.Expires[k] }

// GetUsername implements the Fosite GetUsername session contract.
func (s *Session) GetUsername() string { return "" }

// GetSubject implements the Fosite GetSubject session contract.
func (s *Session) GetSubject() string { return s.Binding.OwnerID }

// Clone implements the Fosite Clone session contract.
func (s *Session) Clone() fosite.Session { return s.cloneSession() }
func (s *Session) cloneSession() *Session {
	out := *s
	out.Expires = map[fosite.TokenType]time.Time{}
	for k, v := range s.Expires {
		out.Expires[k] = v
	}
	out.claims = nil
	return &out
}

// GetJWTHeader implements the Fosite GetJWTHeader session contract.
func (s *Session) GetJWTHeader() *fjwt.Headers {
	h := fjwt.NewHeaders()
	h.Add("typ", "at+jwt")
	h.Add("kid", s.Binding.Kid)
	return h
}

// GetJWTClaims implements the Fosite GetJWTClaims session contract.
func (s *Session) GetJWTClaims() fjwt.JWTClaimsContainer {
	if s.claims == nil {
		if s.JWTID == "" {
			s.JWTID = uuid.NewString()
		}
		if s.IssuedAt.IsZero() {
			s.IssuedAt = time.Now().UTC()
		}
		b := s.Binding
		s.claims = &fjwt.JWTClaims{Subject: b.OwnerID, JTI: s.JWTID, IssuedAt: s.IssuedAt, Extra: map[string]any{"client_id": b.ClientID, "device_id": b.DeviceID, "family_id": b.FamilyID, "token_stage": string(b.Stage), "owner_security_epoch": b.OwnerEpoch, "device_epoch": b.DeviceEpoch, "signing_epoch": b.SigningEpoch, "auth_time": b.AuthTime}}
	}
	return s.claims
}

type savedRequest struct {
	Version                                                            int
	ID, ClientID                                                       string
	RequestedAt                                                        time.Time
	RequestedScopes, GrantedScopes, RequestedAudience, GrantedAudience []string
	Form                                                               map[string][]string
	Session                                                            Session
}

func decode(data []byte) (*fosite.Request, error) {
	var p savedRequest
	if err := json.Unmarshal(data, &p); err != nil || p.Version != 1 {
		return nil, ErrInvalid
	}
	return &fosite.Request{ID: p.ID, Client: &fosite.DefaultClient{ID: p.ClientID}, RequestedAt: p.RequestedAt, RequestedScope: p.RequestedScopes, GrantedScope: p.GrantedScopes, RequestedAudience: p.RequestedAudience, GrantedAudience: p.GrantedAudience, Form: p.Form, Session: &p.Session}, nil
}

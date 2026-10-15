package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iulita-ai/iulita/internal/mobile/devices"
)

// Settings contains persistent-store or runtime configuration.
type Settings struct {
	Scenarios           []devices.Scenario `json:"scenarios,omitempty"`
	Issuer              string             `json:"issuer"`
	Audience            string             `json:"audience"`
	AccountDB           string             `json:"account_db"`
	OAuthDB             string             `json:"oauth_db"`
	RealmDir            string             `json:"realm_dir"`
	TLSCert             string             `json:"tls_cert"`
	TLSKey              string             `json:"tls_key"`
	GRPCAddress         string             `json:"grpc_address"`
	OAuthAddress        string             `json:"oauth_address"`
	ClientID            string             `json:"client_id"`
	RedirectURI         string             `json:"redirect_uri"`
	AndroidPackage      string             `json:"android_package,omitempty"`
	AndroidFingerprints []string           `json:"android_release_fingerprints,omitempty"`
}

// ReadSettings loads and validates explicit runtime settings.
func ReadSettings(path string) (Settings, error) {
	var s Settings
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return s, errors.New("mobile runtime settings unavailable")
	}
	body, err := os.ReadFile(path) //nolint:gosec // Path comes from explicit local runtime configuration, not remote input.
	if err != nil {
		return s, errors.New("mobile runtime settings unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&s) != nil || decoder.Decode(new(any)) != io.EOF {
		return s, errors.New("invalid mobile runtime settings")
	}
	return s, s.Validate()
}

// Validate checks configuration dependencies and required values.
func (s Settings) Validate() error {
	fail := errors.New("invalid mobile runtime settings")
	u, err := url.Parse(s.Issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || s.Audience == "" {
		return fail
	}
	redirect, err := url.Parse(s.RedirectURI)
	if err != nil || redirect.Scheme != "https" || redirect.Host != u.Host || redirect.User != nil || redirect.RawQuery != "" || redirect.Fragment != "" || redirect.Path != "/mobile/oauth/callback" || s.ClientID == "" || len(s.ClientID) > 128 {
		return fail
	}
	for _, path := range []string{s.AccountDB, s.OAuthDB, s.RealmDir, s.TLSCert, s.TLSKey} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fail
		}
	}
	if s.AccountDB == s.OAuthDB || s.TLSCert == s.TLSKey {
		return fail
	}
	for _, address := range []string{s.GRPCAddress, s.OAuthAddress} {
		host, port, err := net.SplitHostPort(address)
		n, e := strconv.Atoi(port)
		if err != nil || net.ParseIP(host) == nil || e != nil || n < 1 || n > 65535 {
			return fail
		}
	}
	if s.GRPCAddress == s.OAuthAddress {
		return fail
	}
	if len(s.AndroidFingerprints) > 2 {
		return fail
	}
	for _, f := range s.AndroidFingerprints {
		parts := strings.Split(f, ":")
		if len(parts) != 32 {
			return fail
		}
		for _, p := range parts {
			if len(p) != 2 || strings.Trim(p, "0123456789ABCDEF") != "" {
				return fail
			}
		}
	}
	if len(s.AndroidFingerprints) > 0 && (s.AndroidPackage == "" || strings.ContainsAny(s.AndroidPackage, " /\\\r\n")) {
		return fail
	}
	return nil
}

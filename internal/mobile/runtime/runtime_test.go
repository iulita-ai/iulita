package runtime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"github.com/iulita-ai/iulita/internal/models"
	legacy "github.com/iulita-ai/iulita/internal/storage/sqlite"
)

type fixtureModel struct{}

func (fixtureModel) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Content: "synthetic"}, nil
}

type fixtureInvoker struct{ snapshot *llm.ProfileSnapshot }

func (f fixtureInvoker) AcquireProfileSnapshot() *llm.ProfileSnapshot { return f.snapshot }
func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}
func runtimeFixture(t *testing.T) (Settings, *legacy.Store, fixtureInvoker, *x509.CertPool) {
	t.Helper()
	dir := t.TempDir()
	db, err := legacy.New(filepath.Join(dir, "main.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = db.RunMigrations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateUser(context.Background(), &domain.User{ID: uuid.NewString(), Username: "owner", PasswordHash: "fixture"}); err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	certTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"mobile.example.test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, _ := x509.MarshalECPrivateKey(key)
	certPath, keyPath := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	s := Settings{Issuer: "https://mobile.example.test", Audience: "iulita-mobile", AccountDB: filepath.Join(dir, "main.sqlite"), OAuthDB: filepath.Join(dir, "realm", "oauth.sqlite"), RealmDir: filepath.Join(dir, "realm", "keys"), TLSCert: certPath, TLSKey: keyPath, GRPCAddress: freeAddress(t), OAuthAddress: freeAddress(t), ClientID: "mobile", RedirectURI: "https://mobile.example.test/mobile/oauth/callback"}
	modelSettings := models.Settings{SchemaVersion: 1, Profiles: models.CandidateProfiles(), Policy: models.Policy{Everyday: "ds-flash"}}
	bindings := map[string]llm.ProfileBinding{}
	for _, p := range modelSettings.Profiles {
		bindings[p.ID] = llm.ProfileBinding{Profile: p, Provider: fixtureModel{}, Eligibility: "production_eligible"}
	}
	snapshot, err := llm.NewProfileSnapshot(1, modelSettings, bindings)
	if err != nil {
		t.Fatal(err)
	}
	return s, db, fixtureInvoker{snapshot}, pool
}
func TestExplicitBootstrapAndTLSRuntimeLifecycle(t *testing.T) {
	s, db, invoker, pool := runtimeFixture(t)
	accounts := browseroauth.PasswordAccounts{Users: db}
	features := config.MobileConfig{ChatEnabled: true, DeviceEnabled: true}
	if _, err := New(context.Background(), s, features, accounts, invoker, "system", "test"); err == nil {
		t.Fatal("startup generated missing keys")
	}
	if err := Initialize(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(context.Background(), s); err != nil {
		t.Fatal("idempotent bootstrap", err)
	}
	r, err := New(context.Background(), s, features, accounts, invoker, "system", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err = New(context.Background(), s, features, accounts, invoker, "system", "test"); !errors.Is(err, oauthstore.ErrConflict) {
		t.Fatal("second runtime lock", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "mobile.example.test", MinVersion: tls.VersionTLS13}, ForceAttemptHTTP2: true}, Timeout: time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, stepErr1 := client.Get("https://" + s.OAuthAddress + "/health/ready")
		if stepErr1 == nil {
			response.Body.Close()
			if response.StatusCode == 204 {
				if response.ProtoMajor != 2 {
					t.Fatal("ALPN h2 missing")
				}
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("runtime readiness unavailable", stepErr1)
		}
		time.Sleep(10 * time.Millisecond)
	}
	request, _ := http.NewRequest("GET", "https://"+s.OAuthAddress+"/.well-known/oauth-authorization-server", http.NoBody)
	request.Host = "mobile.example.test"
	response, err := client.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var metadata map[string]any
	err = json.NewDecoder(response.Body).Decode(&metadata)
	response.Body.Close()
	if err != nil || metadata["issuer"] != s.Issuer {
		cancel()
		t.Fatal("TLS metadata issuer", err)
	}
	metricsResponse, metricsErr := client.Get("https://" + s.OAuthAddress + "/metrics")
	if metricsErr != nil {
		cancel()
		t.Fatal(metricsErr)
	}
	metricsBody, metricsErr := io.ReadAll(metricsResponse.Body)
	metricsResponse.Body.Close()
	if metricsErr != nil || metricsResponse.StatusCode != 200 || !strings.Contains(string(metricsBody), "iulita_mobile_rpc_active") || strings.Contains(string(metricsBody), "owner_id") {
		cancel()
		t.Fatal("bounded metadata diagnostics", metricsErr)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not shut down")
	}
	if r.ready.Load() {
		t.Fatal("stopped runtime still ready")
	}
	client.CloseIdleConnections()
}
func TestRuntimeSettingsRejectCrossHostRedirectAndUnknownFields(t *testing.T) {
	s, _, _, _ := runtimeFixture(t)
	s.RedirectURI = "https://dev.example.test/mobile/oauth/callback"
	if s.Validate() == nil {
		t.Fatal("development redirect crossed production issuer")
	}
	body, _ := json.Marshal(s)
	path := filepath.Join(t.TempDir(), "runtime.json")
	body = append(body[:len(body)-1], []byte(`,"dashboard_secret":"forbidden"}`)...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSettings(path); err == nil {
		t.Fatal("unknown runtime property accepted")
	}
}

// Package runtime assembles the mobile backend without mutating legacy channel
// routing. New never generates keys, registers clients or starts a listener.
package runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/mobile/ephemeral"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	"github.com/iulita-ai/iulita/internal/mobile/transport"
	"github.com/ory/fosite"
)

var scopes = []string{"chats:read", "chats:write", "devices:read", "devices:manage", "devices:receive", "devices:sync"}

// Runtime owns the mobile execution lifecycle and its fencing state.
type Runtime struct {
	settings Settings
	lock     *oauthstore.RealmLock
	oauth    *oauthstore.Store
	chats    *conversations.Repository
	devices  *devices.Store
	content  *ephemeral.Store
	engine   *engine.Engine
	grpc     *transport.Server
	http     *http.Server
	ready    atomic.Bool
	close    sync.Once
}

func registered(s Settings) fosite.DefaultClient {
	return fosite.DefaultClient{ID: s.ClientID, Public: true, RedirectURIs: []string{s.RedirectURI}, GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, Scopes: scopes, Audience: []string{s.Audience}}
}

// Initialize is an explicit offline bootstrap command, never startup recovery.
// Existing registration is compared exactly; redirects/scopes are not replaced.
func Initialize(ctx context.Context, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if info, err := os.Stat(s.AccountDB); err != nil || !info.Mode().IsRegular() {
		return errors.New("existing Iulita account database required")
	}
	account, err := sqlitedb.Open(ctx, s.AccountDB, false)
	if err != nil {
		return err
	}
	var users int
	err = account.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT id,password_hash,disabled,must_change_pass,security_epoch FROM users LIMIT 1)`).Scan(&users)
	account.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return errors.New("migrated Iulita account database required")
	}
	realm, err := oauthstore.InitializeRealm(s.RealmDir, s.Issuer, s.Audience)
	if err != nil {
		return err
	}
	lock, err := oauthstore.LockRealm(s.RealmDir)
	if err != nil {
		return err
	}
	defer lock.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	if checkErr2 := os.MkdirAll(filepath.Dir(s.OAuthDB), 0o700); checkErr2 != nil {
		return checkErr2
	}
	store, err := oauthstore.Initialize(ctx, s.OAuthDB, realm)
	if err != nil {
		return err
	}
	defer store.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	expected := registered(s)
	old, err := store.GetClient(ctx, s.ClientID)
	if err == nil {
		typed, ok := old.(*fosite.DefaultClient)
		if !ok || !reflect.DeepEqual(*typed, expected) {
			return errors.New("mobile public client registration differs; explicit review required")
		}
		return nil
	}
	if !errors.Is(err, fosite.ErrNotFound) {
		return err
	}
	return store.RegisterClient(ctx, expected)
}

// New constructs the subsystem with explicit dependencies and limits.
func New(ctx context.Context, s Settings, features config.MobileConfig, accounts browseroauth.Accounts, models llm.ProfileInvoker, system, version string) (result *Runtime, err error) {
	if checkErr1 := s.Validate(); checkErr1 != nil {
		return nil, checkErr1
	}
	if models == nil || (reflect.ValueOf(models).Kind() == reflect.Pointer && reflect.ValueOf(models).IsNil()) || accounts == nil || models.AcquireProfileSnapshot() == nil {
		return nil, errors.New("mobile account/model authority required")
	}
	if !features.ChatEnabled && !features.DeviceEnabled {
		return nil, errors.New("no mobile backend requested")
	}
	r := &Runtime{settings: s}
	defer func() {
		if err != nil {
			r.Close()
		}
	}()
	r.lock, err = oauthstore.LockRealm(s.RealmDir)
	if err != nil {
		return nil, err
	}
	realm, err := oauthstore.LoadRealm(s.RealmDir, s.Issuer, s.Audience)
	if err != nil {
		return nil, err
	}
	r.oauth, err = oauthstore.Open(ctx, s.OAuthDB, realm)
	if err != nil {
		return nil, err
	}
	client, err := r.oauth.GetClient(ctx, s.ClientID)
	if err != nil {
		return nil, errors.New("mobile public client not initialized")
	}
	expected := registered(s)
	typed, ok := client.(*fosite.DefaultClient)
	if !ok || !reflect.DeepEqual(*typed, expected) {
		return nil, errors.New("mobile public client registration mismatch")
	}
	keyInfo, err := os.Lstat(s.TLSKey)
	if err != nil || !keyInfo.Mode().IsRegular() || keyInfo.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("mobile TLS key must be a private regular file")
	}
	cert, err := tls.LoadX509KeyPair(s.TLSCert, s.TLSKey)
	if err != nil {
		return nil, errors.New("mobile TLS certificate unavailable")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, errors.New("mobile TLS certificate unavailable")
	}
	origin, parseErr := url.Parse(s.Issuer)
	if parseErr != nil {
		return nil, errors.New("invalid mobile issuer")
	}
	if leaf.VerifyHostname(origin.Hostname()) != nil || !leaf.NotAfter.After(time.Now()) || leaf.NotBefore.After(time.Now()) {
		return nil, errors.New("mobile TLS certificate does not cover the issuer")
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	signer, operationErr1 := realm.CursorSigner()
	if operationErr1 != nil {
		return nil, operationErr1
	}
	r.chats, err = conversations.OpenExisting(ctx, s.AccountDB, signer)
	if err != nil {
		return nil, err
	}
	r.content, err = ephemeral.New()
	if err != nil {
		return nil, err
	}
	var tools []engine.Tool
	if features.DeviceEnabled {
		r.devices, err = devices.Open(ctx, s.OAuthDB, r.oauth, accounts, r.chats, r.content)
		if err != nil {
			return nil, err
		}
		tools = r.devices.Tools()
	}
	r.engine, err = engine.New(r.chats, engine.Models(models), system, tools)
	if err != nil {
		return nil, err
	}
	r.engine.VerifyAuthority = r.oauth.ValidateAuthority
	if memory, ok := accounts.(engine.MemorySource); ok {
		r.engine.AccountMemory = memory
	}
	r.grpc, err = transport.New(transport.Config{TLS: tlsConfig, OAuth: r.oauth, Accounts: accounts, Chats: r.chats, Engine: r.engine, Devices: r.devices, Features: features, Version: version, Ready: r.ready.Load})
	if err != nil {
		return nil, err
	}
	browser, err := browseroauth.New(browseroauth.Config{Store: r.oauth, Accounts: accounts, Scopes: scopes})
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", r.metrics)
	mux.Handle("/oauth2/", browser.Handler())
	mux.Handle("/.well-known/oauth-authorization-server", browser.Handler())
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, _ *http.Request) {
		if !r.ready.Load() {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/mobile/oauth/callback", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if _, writeErr := io.WriteString(w, "Вернитесь в приложение Iulita. Не пересылайте адрес этой страницы или код входа."); writeErr != nil {
			return
		}
	})
	mux.HandleFunc("/.well-known/assetlinks.json", func(w http.ResponseWriter, _ *http.Request) {
		if len(s.AndroidFingerprints) == 0 {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if writeErr := json.NewEncoder(w).Encode([]any{map[string]any{"relation": []string{"delegate_permission/common.handle_all_urls"}, "target": map[string]any{"namespace": "android_app", "package_name": s.AndroidPackage, "sha256_cert_fingerprints": s.AndroidFingerprints}}}); writeErr != nil {
			return
		}
	})
	r.http = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if !r.ready.Load() && request.URL.Path != "/health/live" && request.URL.Path != "/health/ready" {
			w.WriteHeader(503)
			return
		}
		mux.ServeHTTP(w, request)
	}), TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	return r, nil
}

// Run owns all listeners/workers and closes on the first fatal service error.
// All loggable errors are bounded categories; request/response bodies are absent.
func (r *Runtime) Run(ctx context.Context) error {
	grpcListener, err := net.Listen("tcp", r.settings.GRPCAddress)
	if err != nil {
		return errors.New("mobile gRPC listener unavailable")
	}
	defer grpcListener.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	httpListener, err := net.Listen("tcp", r.settings.OAuthAddress)
	if err != nil {
		return errors.New("mobile OAuth listener unavailable")
	}
	defer httpListener.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
	root, cancel := context.WithCancel(ctx)
	defer cancel()
	r.http.BaseContext = func(net.Listener) context.Context { return root }
	var wg sync.WaitGroup
	failed := make(chan struct{}, 5)
	start := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if fn() != nil && root.Err() == nil {
				select {
				case failed <- struct{}{}:
				default:
				}
			}
		}()
	}
	start(func() error { return r.grpc.Serve(grpcListener) })
	start(func() error { return r.http.ServeTLS(httpListener, "", "") })
	start(func() error { return r.engine.Run(root) })
	start(func() error { return r.oauth.RunMaintenance(root) })
	start(func() error { r.content.Run(root); return nil })
	if r.devices != nil {
		start(func() error { return r.devices.Run(root) })
	}
	select {
	case <-r.engine.Ready():
		r.ready.Store(true)
	case <-ctx.Done():
		cancel()
	case <-failed:
		cancel()
	}
	var stopped error
	select {
	case <-root.Done():
		stopped = root.Err()
	case <-failed:
		stopped = errors.New("mobile service stopped")
	}
	r.ready.Store(false)
	cancel()
	shutdown, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	r.grpc.Shutdown(shutdown)
	shutdownErr := r.http.Shutdown(shutdown)
	wg.Wait()
	return errors.Join(stopped, shutdownErr)
}

// Close releases the resources owned by this instance.
func (r *Runtime) Close() {
	if r == nil {
		return
	}
	r.close.Do(func() {
		r.ready.Store(false)
		if r.devices != nil {
			_ = r.devices.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
		}
		if r.chats != nil {
			_ = r.chats.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
		}
		if r.oauth != nil {
			_ = r.oauth.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
		}
		if r.lock != nil {
			_ = r.lock.Close() //nolint:errcheck // Shutdown cleanup; closing the descriptor releases the file lock.
		}
	})
}

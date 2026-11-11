// gatewaycheck is a disposable TLS1.3/HTTP2 backend for gateway image CI.
package main

import (
	"bytes"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serve() error {
	data, err := os.ReadFile("/fixture/server.crt")
	if err != nil {
		return err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return fmt.Errorf("fixture certificate missing")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	key, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("fixture RSA key missing")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Iulita-Smoke-Backend", "verified")
		if r.Host != "mobile.fixture" || r.TLS == nil || r.TLS.Version != tls.VersionTLS13 {
			http.Error(w, "upstream host or TLS changed", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			if encodeErr := json.NewEncoder(w).Encode(map[string]string{"issuer": "https://mobile.fixture", "jwks_uri": "https://mobile.fixture/oauth2/jwks"}); encodeErr != nil {
				return
			}
		case "/oauth2/jwks":
			w.Header().Set("Content-Type", "application/json")
			if encodeErr := json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "fixture", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}}); encodeErr != nil {
				return
			}
		case "/iulita.mobile.v1.Smoke/Check":
			if r.ProtoMajor != 2 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
				http.Error(w, "gRPC HTTP2 required", http.StatusBadRequest)
				return
			}
			body, readErr := io.ReadAll(io.LimitReader(r.Body, 16))
			if readErr != nil || r.Method != http.MethodPost || !bytes.Equal(body, []byte{0, 0, 0, 0, 0}) {
				http.Error(w, "gRPC request frame changed", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/grpc")
			w.Header().Set("Trailer", "Grpc-Status")
			w.WriteHeader(http.StatusOK)
			// Send streaming headers before completion, without an inferred
			// Content-Length that lets clients finish before reading trailers.
			if flushErr := http.NewResponseController(w).Flush(); flushErr != nil {
				return
			}
			if r.Header.Get("Authorization") != "Bearer isolated-fixture" {
				w.Header().Set("Grpc-Status", "16")
				return
			}
			// A valid uncompressed protobuf gRPC frame (field1 enum SERVING=1).
			if _, writeErr := w.Write([]byte{0, 0, 0, 0, 2, 8, 1}); writeErr != nil {
				return
			}
			w.Header().Set("Grpc-Status", "0")
		default:
			http.Error(w, "fixture authorization required", http.StatusUnauthorized)
		}
	})
	server := &http.Server{Addr: ":8444", Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	return server.ListenAndServeTLS("/fixture/server.crt", "/fixture/server.key")
}

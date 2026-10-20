package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"
)

// workerTLSConfig validates startup credentials and reloads the projected pair
// for every new handshake. A broken rotation fails closed instead of serving a
// cached, potentially expired certificate. Established connections are unchanged.
func workerTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	load := func() (*tls.Certificate, error) {
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, errors.New("worker TLS certificate/key unavailable or invalid")
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return nil, errors.New("worker TLS certificate invalid")
		}
		now := time.Now()
		if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
			return nil, errors.New("worker TLS certificate outside validity period")
		}
		pair.Leaf = leaf
		return &pair, nil
	}
	if _, err := load(); err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, SessionTicketsDisabled: true, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return load() }}, nil
}

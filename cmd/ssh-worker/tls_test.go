package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testTLSAuthority struct {
	cert  *x509.Certificate
	key   ed25519.PrivateKey
	roots *x509.CertPool
}

func newTestTLSAuthority(t *testing.T) testTLSAuthority {
	t.Helper()
	public, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "worker-test-ca"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, e := x509.CreateCertificate(rand.Reader, template, template, public, key)
	if e != nil {
		t.Fatal(e)
	}
	cert, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return testTLSAuthority{cert: cert, key: key, roots: roots}
}
func (a testTLSAuthority) pair(t *testing.T, serial int64, expiry time.Time) (certificatePEM, privateKeyPEM []byte) {
	t.Helper()
	public, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{"worker.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: expiry, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, a.cert, public, a.key)
	if e != nil {
		t.Fatal(e)
	}
	private, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
}
func writeTLSPair(t *testing.T, dir string, cert, key []byte) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(dir, "tls.crt"), cert, 0o600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "tls.key"), key, 0o600); e != nil {
		t.Fatal(e)
	}
}
func TestWorkerTLSReloadsAtomicProjectedPairAndFailsClosed(t *testing.T) {
	ca := newTestTLSAuthority(t)
	dir := t.TempDir()
	one := filepath.Join(dir, "one")
	two := filepath.Join(dir, "two")
	bad := filepath.Join(dir, "bad")
	for _, p := range []string{one, two, bad} {
		if e := os.Mkdir(p, 0o700); e != nil {
			t.Fatal(e)
		}
	}
	cert, key := ca.pair(t, 11, time.Now().Add(time.Hour))
	writeTLSPair(t, one, cert, key)
	cert, key = ca.pair(t, 22, time.Now().Add(time.Hour))
	writeTLSPair(t, two, cert, key)
	writeTLSPair(t, bad, cert, []byte("PRIVATE-KEY-CANARY invalid"))
	current := filepath.Join(dir, "current")
	if e := os.Symlink(one, current); e != nil {
		t.Fatal(e)
	}
	config, e := workerTLSConfig(filepath.Join(current, "tls.crt"), filepath.Join(current, "tls.key"))
	if e != nil {
		t.Fatal(e)
	}
	if config.MinVersion != tls.VersionTLS12 || !config.SessionTicketsDisabled {
		t.Fatal("unsafe TLS protocol/resumption policy")
	}
	listener, e := tls.Listen("tcp", "127.0.0.1:0", config)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			if secure, ok := c.(*tls.Conn); ok {
				_ = secure.SetDeadline(time.Now().Add(2 * time.Second))
				_ = secure.Handshake()
			}
			_ = c.Close()
		}
	}()
	defer func() { _ = listener.Close(); <-done }()
	connect := func() (int64, error) {
		c, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", listener.Addr().String(), &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "worker.test", RootCAs: ca.roots})
		if err != nil {
			return 0, err
		}
		defer c.Close()
		return c.ConnectionState().PeerCertificates[0].SerialNumber.Int64(), nil
	}
	serial, e := connect()
	if e != nil || serial != 11 {
		t.Fatalf("initial TLS serial%d error%v", serial, e)
	}
	replace := func(target string) {
		t.Helper()
		temp := filepath.Join(dir, "next")
		if err := os.Symlink(target, temp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temp, current); err != nil {
			t.Fatal(err)
		}
	}
	replace(two)
	serial, e = connect()
	if e != nil || serial != 22 {
		t.Fatalf("rotated TLS serial%d error%v", serial, e)
	}
	replace(bad)
	if _, e = connect(); e == nil {
		t.Fatal("broken projected pair silently served cached leaf")
	}
	replace(one)
	if serial, e = connect(); e != nil || serial != 11 {
		t.Fatal("valid pair was not recovered", e)
	}
}
func TestWorkerTLSRejectsInvalidStartupAndExpiredRotation(t *testing.T) {
	ca := newTestTLSAuthority(t)
	dir := t.TempDir()
	cert, key := ca.pair(t, 33, time.Now().Add(time.Hour))
	writeTLSPair(t, dir, cert, key)
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	config, e := workerTLSConfig(certFile, keyFile)
	if e != nil {
		t.Fatal(e)
	}
	expiredCert, expiredKey := ca.pair(t, 44, time.Now().Add(-time.Minute))
	writeTLSPair(t, dir, expiredCert, expiredKey)
	if _, e = config.GetCertificate(&tls.ClientHelloInfo{}); e == nil {
		t.Fatal("expired rotation accepted")
	}
	if _, e = workerTLSConfig(certFile, keyFile); e == nil {
		t.Fatal("expired startup pair accepted")
	}
	writeTLSPair(t, dir, cert, []byte("PRIVATE-KEY-CANARY"))
	if _, e = workerTLSConfig(certFile, keyFile); e == nil || strings.Contains(e.Error(), "PRIVATE-KEY-CANARY") {
		t.Fatal("invalid startup pair accepted or private key leaked")
	}
}

package remoteaccess

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func testSFTPServer(t *testing.T, allowExec ...bool) TransportEndpoint {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(key)
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			raw, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				defer raw.Close()
				conn, chans, requests, e := ssh.NewServerConn(raw, cfg)
				if e != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for ch := range chans {
					channel, reqs, e := ch.Accept()
					if e != nil {
						return
					}
					go func() {
						defer channel.Close()
						for req := range reqs {

							if req.Type == "exec" && len(allowExec) > 0 && allowExec[0] {
								var p struct{ Command string }
								if ssh.Unmarshal(req.Payload, &p) != nil {
									_ = req.Reply(false, nil)
									continue
								}
								_ = req.Reply(true, nil)
								command := exec.Command("sh", "-c", p.Command)
								command.Stdout = channel
								command.Stderr = channel.Stderr()
								status := uint32(0)
								if err := command.Run(); err != nil {
									status = 1
								}
								_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
								return
							}
							if req.Type != "subsystem" {
								req.Reply(false, nil)
								continue
							}
							var p struct{ Name string }
							ssh.Unmarshal(req.Payload, &p)
							if p.Name != "sftp" {
								req.Reply(false, nil)
								continue
							}
							req.Reply(true, nil)
							server, e := sftp.NewServer(channel)
							if e != nil {
								return
							}
							_ = server.Serve()
							_ = server.Close()
							return
						}
					}()
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.Atoi(port)
	return TransportEndpoint{Address: host, Port: n, Username: "test", Password: "unused", HostKeys: []string{string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}}
}
func TestNativeSFTPTransfer(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ep := testSFTPServer(t)
	data := []byte("private file\n")
	run := func(op string, f FileRequest) (TransportResult, error) {
		return (NativeTransport{}).Run(context.Background(), TransportRequest{Endpoints: []TransportEndpoint{ep}, Operation: op, File: &f, Timeout: time.Second * 5})
	}
	r, e := run("file_put", FileRequest{Root: root, Path: "new", Data: data, SHA256: sha256Hex(data)})
	if e != nil || r.File.Size != int64(len(data)) {
		t.Fatalf("put: %#v %v", r, e)
	}
	r, e = run("file_get", FileRequest{Root: root, Path: "new", SHA256: sha256Hex(data)})
	if e != nil || !bytes.Equal(r.File.Data, data) {
		t.Fatalf("get: %#v %v", r, e)
	}
	if _, e = run("file_put", FileRequest{Root: root, Path: "new", Data: data, SHA256: sha256Hex(data), Overwrite: true}); e == nil {
		t.Fatal("overwrote existing target")
	}
	for _, p := range []string{"../escape", "/absolute", "a/../b"} {
		if _, e = run("file_stat", FileRequest{Root: root, Path: p}); e == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	if e = os.Symlink("new", filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e = run("file_get", FileRequest{Root: root, Path: "link"}); e == nil {
		t.Fatal("followed symlink")
	}
	if e = os.WriteFile(filepath.Join(root, "large"), make([]byte, FileLimit+1), 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e = run("file_get", FileRequest{Root: root, Path: "large"}); e == nil {
		t.Fatal("accepted oversized file")
	}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if len(entry.Name()) > 15 && entry.Name()[:15] == ".iulita-upload-" {
			t.Fatal("staging leak")
		}
	}
}

func TestNativeSFTPRejectsUntrustedRoots(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	endpoint := testSFTPServer(t)
	run := func(root string) error {
		_, runErr := (NativeTransport{}).Run(context.Background(), TransportRequest{Endpoints: []TransportEndpoint{endpoint}, Operation: "file_put", File: &FileRequest{Root: root, Path: "new", Data: []byte("test"), SHA256: sha256Hex([]byte("test"))}, Timeout: 5 * time.Second})
		return runErr
	}
	for _, mode := range []os.FileMode{0o755, 0o777} {
		if err = os.Chmod(root, mode); err != nil {
			t.Fatal(err)
		}
		if run(root) == nil {
			t.Fatalf("accepted root mode %o", mode)
		}
	}
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "writable")
	if err = os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "private")
	if err = os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if run(child) == nil {
		t.Fatal("accepted non-sticky writable ancestor")
	}
	if err = os.Chmod(parent, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	if err = run(child); err != nil {
		t.Fatalf("rejected sticky ancestor: %v", err)
	}
}

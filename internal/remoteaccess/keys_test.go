package remoteaccess

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func samplePublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(key))
}
func runKeyScript(t *testing.T, home, key, id string, remove bool) error {
	t.Helper()
	script, err := managedKeyScript(key, id, remove)
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	script = strings.Replace(script, "home = pwd.getpwuid(uid).pw_dir", "home = os.environ['HOME']", 1)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Log(string(out))
	}
	return err
}
func TestManagedKeyScriptPreservesForeignAndRemovesExact(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := `restrict,command="echo untouched" ` + strings.TrimSpace(samplePublicKey(t)) + " foreign\n"
	path := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(path, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	key := samplePublicKey(t)
	id := strings.Repeat("a", 32)
	for i := 0; i < 2; i++ {
		if err := runKeyScript(t, home, key, id, false); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), foreign) || strings.Count(string(data), "iulita:"+id) != 1 {
		t.Fatal("foreign line changed or duplicate installation")
	}
	if err := runKeyScript(t, home, key, id, true); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != foreign {
		t.Fatal("removal changed foreign lines")
	}
	if err := runKeyScript(t, home, key, id, true); err == nil {
		t.Fatal("missing managed key silently removed")
	}
}
func TestManagedKeyScriptRejectsSymlinksAndPermissions(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	for _, scenario := range []string{"directory_symlink", "file_symlink", "open_permissions"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".ssh")
			outside := filepath.Join(t.TempDir(), "untouched")
			os.WriteFile(outside, []byte("untouched"), 0o600)
			switch scenario {
			case "directory_symlink":
				os.Symlink(filepath.Dir(outside), dir)
			case "file_symlink":
				os.Mkdir(dir, 0o700)
				os.Symlink(outside, filepath.Join(dir, "authorized_keys"))
			case "open_permissions":
				os.Mkdir(dir, 0o700)
				os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte("foreign"), 0o644)
			}
			if err := runKeyScript(t, home, samplePublicKey(t), strings.Repeat("b", 32), false); err == nil {
				t.Fatal("unsafe target accepted")
			}
			data, _ := os.ReadFile(outside)
			if string(data) != "untouched" {
				t.Fatal("external file changed")
			}
		})
	}
}
func TestManagedKeyScriptRejectsOptionsAndMarkerInjection(t *testing.T) {
	for _, p := range []struct{ key, id string }{{"command=\"unsafe\" " + samplePublicKey(t), strings.Repeat("a", 32)}, {samplePublicKey(t), "'\nunsafe"}} {
		if _, err := managedKeyScript(p.key, p.id, false); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}
func keyGrant(t *testing.T, s *Service, sc Scope, route, capability string) {
	t.Helper()
	if _, err := s.PutGrant(sc, Grant{CallerID: sc.CallerID, RouteID: route, Capability: capability, Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}
func TestGeneratedIdentityAndKeyLifecycle(t *testing.T) {
	s, sc, req, tr := setupService(t)
	keyGrant(t, s, sc, req.RouteID, "key_install")
	identity, err := s.GenerateIdentity(context.Background(), sc, Identity{Username: "alice", Enabled: true})
	if err != nil || identity.PublicKey == "" || identity.PublicFingerprint == "" {
		t.Fatalf("%+v %v", identity, err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil || strings.Contains(string(data), "PRIVATE KEY") {
		t.Fatal("private key leaked")
	}
	approved := func(context.Context, Approval) (bool, error) { return true, nil }
	installed, err := s.InstallPublicKey(context.Background(), sc, req.RouteID, identity.ID, approved)
	if err != nil || installed.State != "installed_unverified" {
		t.Fatalf("%+v %v", installed, err)
	}
	route := s.data.Routes[req.RouteID]
	route.Hops = append([]Hop(nil), route.Hops...)
	route.ID = ""
	route.Hops[len(route.Hops)-1].IdentityID = identity.ID
	route, err = s.PutRoute(sc, route)
	if err != nil {
		t.Fatal(err)
	}
	keyGrant(t, s, sc, route.ID, "key_install")
	verified, err := s.VerifyInstallation(context.Background(), sc, installed.ID, route.ID, approved)
	if err != nil || verified.State != "verified" {
		t.Fatalf("%+v %v", verified, err)
	}
	if tr.calls != 2 {
		t.Fatalf("expected install and independent login: %d", tr.calls)
	}
	if _, err = s.RemovePublicKey(context.Background(), sc, installed.ID, installed.ID, route.ID, approved); err == nil {
		t.Fatal("last key removal accepted")
	}
	if _, err = s.VerifyInstallation(context.Background(), Scope{"other", sc.CallerID}, installed.ID, route.ID, approved); err == nil {
		t.Fatal("cross-owner verification")
	}
}

func TestKeyRotationVerifiesBeforeRemoval(t *testing.T) {
	s, sc, req, tr := setupService(t)
	approved := func(context.Context, Approval) (bool, error) { return true, nil }
	keyGrant(t, s, sc, req.RouteID, "key_install")
	oldIdentity, err := s.GenerateIdentity(context.Background(), sc, Identity{Username: "alice", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.InstallPublicKey(context.Background(), sc, req.RouteID, oldIdentity.ID, approved)
	if err != nil {
		t.Fatal(err)
	}
	nextIdentity, err := s.GenerateIdentity(context.Background(), sc, Identity{Username: "alice", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := s.Snapshot(sc)
	route := snapshot.Routes[req.RouteID]
	route.Hops = append([]Hop(nil), route.Hops...)
	route.ID = ""
	route.Hops[len(route.Hops)-1].IdentityID = nextIdentity.ID
	route, err = s.PutRoute(sc, route)
	if err != nil {
		t.Fatal(err)
	}
	keyGrant(t, s, sc, route.ID, "key_install")
	keyGrant(t, s, sc, route.ID, "key_remove")
	next, err := s.RotatePublicKey(context.Background(), sc, old.ID, req.RouteID, nextIdentity.ID, route.ID, approved)
	if err != nil || next.State != "verified" {
		t.Fatalf("rotation: %+v %v", next, err)
	}
	snapshot = s.Snapshot(sc)
	if snapshot.Installations[old.ID].State != "removed" || tr.calls != 4 {
		t.Fatalf("old state=%s calls=%d", snapshot.Installations[old.ID].State, tr.calls)
	}
}
func TestKeyInstallationUnknownAndCapabilityBoundaries(t *testing.T) {
	s, sc, req, tr := setupService(t)
	identity, err := s.GenerateIdentity(context.Background(), sc, Identity{Username: "alice", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	approved := func(context.Context, Approval) (bool, error) { return true, nil }
	if _, err = s.InstallPublicKey(context.Background(), sc, req.RouteID, identity.ID, approved); err == nil || tr.calls != 0 {
		t.Fatal("exec grant authorized install")
	}
	keyGrant(t, s, sc, req.RouteID, "key_install")
	tr.run = func(context.Context) (TransportResult, error) {
		return TransportResult{Dispatched: true, ErrorCode: "unknown_outcome"}, nil
	}
	installed, err := s.InstallPublicKey(context.Background(), sc, req.RouteID, identity.ID, approved)
	if err != nil || installed.State != "install_unknown" {
		t.Fatalf("%+v %v", installed, err)
	}
	if s.Snapshot(sc).Installations[installed.ID].State != "install_unknown" {
		t.Fatal("inventory lost ambiguous remote key")
	}
	if _, err = s.Execute(context.Background(), sc, ExecuteRequest{RouteID: req.RouteID, Command: "arbitrary", Capability: "key_install"}, approved); err == nil {
		t.Fatal("arbitrary command used key-install grant")
	}
}

func TestManagedKeyInspectionIsReadOnlyAndDistinguishesAbsent(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, id := samplePublicKey(t), strings.Repeat("c", 32)
	inspect := func() int {
		t.Helper()
		script, inspectionErr := managedKeyInspectionScript(key, id)
		if inspectionErr != nil {
			t.Fatal(inspectionErr)
		}
		script = strings.Replace(script, "home = pwd.getpwuid(uid).pw_dir", "home = os.environ['HOME']", 1)
		command := exec.Command("sh", "-c", script)
		command.Env = append(os.Environ(), "HOME="+home)
		inspectionErr = command.Run()
		if inspectionErr == nil {
			return 0
		}
		var exit *exec.ExitError
		if errors.As(inspectionErr, &exit) {
			return exit.ExitCode()
		}
		t.Fatal(inspectionErr)
		return -1
	}
	if status := inspect(); status != 3 {
		t.Fatalf("absent status=%d", status)
	}
	if _, err = os.Stat(filepath.Join(home, ".ssh")); !os.IsNotExist(err) {
		t.Fatal("inspection created remote directory")
	}
	if err = runKeyScript(t, home, key, id, false); err != nil {
		t.Fatal(err)
	}
	if status := inspect(); status != 0 {
		t.Fatalf("present status=%d", status)
	}
	if err = runKeyScript(t, home, key, id, true); err != nil {
		t.Fatal(err)
	}
	if status := inspect(); status != 3 {
		t.Fatalf("removed status=%d", status)
	}
}
func TestReconcileUnknownRemovalWithoutMutationReplay(t *testing.T) {
	s, sc, req, tr := setupService(t)
	keyGrant(t, s, sc, req.RouteID, "key_install")
	keyGrant(t, s, sc, req.RouteID, "key_remove")
	identity, err := s.GenerateIdentity(context.Background(), sc, Identity{Username: "alice", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	approved := func(context.Context, Approval) (bool, error) { return true, nil }
	installed, err := s.InstallPublicKey(context.Background(), sc, req.RouteID, identity.ID, approved)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	installed.State = "remove_unknown"
	err = s.saveInstallation(installed)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	tr.run = func(context.Context) (TransportResult, error) {
		code := 3
		return TransportResult{Dispatched: true, ExitCode: &code}, nil
	}
	reconciled, err := s.ReconcilePublicKey(context.Background(), sc, installed.ID, req.RouteID, approved)
	if err != nil || reconciled.State != "removed" {
		t.Fatalf("%+v %v", reconciled, err)
	}
	artifact, err := s.Artifact(sc, s.Snapshot(sc).Executions[reconciled.ExecutionID].ArtifactID)
	if err != nil || strings.Contains(artifact.Command, "os.rename(") {
		t.Fatal("reconciliation replayed key mutation")
	}
}

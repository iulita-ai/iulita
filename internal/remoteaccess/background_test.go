package remoteaccess

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBackgroundHelperLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed jobs require Linux procfs")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 required for managed job integration test")
	}
	root := t.TempDir()
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "helper.py")
	if err = os.WriteFile(helper, []byte(backgroundHelper), 0o600); err != nil {
		t.Fatal(err)
	}
	call := func(op, id, command string) BackgroundResult {
		t.Helper()
		out, e := exec.Command(python, helper, root, op, id, base64.StdEncoding.EncodeToString([]byte(command))).CombinedOutput()
		if e != nil {
			t.Fatalf("%s: %v %s", op, e, out)
		}
		var r BackgroundResult
		if e = json.Unmarshal(out, &r); e != nil {
			t.Fatalf("response %s: %v", out, e)
		}
		return r
	}
	id := "finished_job_00001"
	r := call("start", id, "exit 7")
	for i := 0; i < 100; i++ {
		r = call("status", id, "")
		if r.State == "exited" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r.State != "exited" || r.ExitCode == nil || *r.ExitCode != 7 {
		t.Fatalf("exit state %#v", r)
	}
	// A fresh helper process represents worker restart; status only reads durable state.
	r = call("status", id, "")
	if r.State != "exited" {
		t.Fatal(r)
	}
	id = "cancelled_job_0001"
	r = call("start", id, "sleep 120")
	if r.State != "running" {
		t.Fatal(r)
	}
	t.Cleanup(func() { exec.Command(python, helper, root, "cancel", id, "").Run() })
	r = call("cancel", id, "")
	if r.State != "cancelled" {
		t.Fatal(r)
	}
	r = call("status", id, "")
	if r.State != "cancelled" {
		t.Fatal(r)
	}
	// Never replay or replace an existing job ID.
	if err = exec.Command(python, helper, root, "start", id, base64.StdEncoding.EncodeToString([]byte("true"))).Run(); err == nil {
		t.Fatal("replayed job")
	}
}
func TestBackgroundHelperSyntax(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	c := exec.Command(python, "-c", "import ast,sys;ast.parse(sys.stdin.read())")
	c.Stdin = strings.NewReader(backgroundHelper)
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("python syntax: %s %v", out, e)
	}
}

// Exercises the exact native transport over an actual SSH session plus SFTP,
// including helper installation and a fresh connection for every status query.
func TestNativeBackgroundSSHIntegration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed jobs require Linux procfs")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatal("python3 required")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	endpoint := testSFTPServer(t, true)
	run := func(op, id, command string) BackgroundResult {
		t.Helper()
		r, err := (NativeTransport{}).Run(context.Background(), TransportRequest{Endpoints: []TransportEndpoint{endpoint}, Operation: "background_" + op, Background: &BackgroundRequest{Root: root, JobID: id, Command: command}, Timeout: 10 * time.Second})
		if err != nil {
			t.Fatalf("%s: %v %#v", op, err, r)
		}
		if r.Background == nil {
			t.Fatal("missing background result")
		}
		return *r.Background
	}
	id := "integration_exit_001"
	if r := run("install", id, ""); r.State != "installed" {
		t.Fatal(r)
	}
	run("start", id, "exit 7")
	var r BackgroundResult
	for i := 0; i < 100; i++ {
		r = run("status", id, "")
		if r.State == "exited" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r.State != "exited" || r.ExitCode == nil || *r.ExitCode != 7 {
		t.Fatal(r)
	}
	id = "integration_cancel_001"
	r = run("start", id, "trap '' TERM; sleep 120 & wait")
	pid := r.PID
	r = run("cancel", id, "")
	if r.State != "cancelled" {
		t.Fatal(r)
	}
	// Verify the cancelled process group has no live members, even when TERM was ignored.
	probe := exec.Command("python3", "-c", `import os,sys
for entry in os.listdir('/proc'):
 if not entry.isdigit():continue
 try:
  f=open('/proc/'+entry+'/stat').read().rsplit(')',1)[1].split()
  if int(f[2])==int(sys.argv[1]) and f[0] not in ('Z','X'):raise SystemExit(1)
 except FileNotFoundError:pass
`, strconv.Itoa(pid))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("live process after cancelled: %v %s", err, out)
	}
	if r = run("status", id, ""); r.State != "cancelled" {
		t.Fatal(r)
	}
	// Shell exit must not abandon its still-running original process group.
	id = "integration_shell_bg_001"
	r = run("start", id, "sleep 120 &")
	time.Sleep(150 * time.Millisecond)
	r = run("status", id, "")
	if r.State != "running" {
		t.Fatalf("background child abandoned after shell exit: %#v", r)
	}
	r = run("cancel", id, "")
	if r.State != "cancelled" {
		t.Fatal(r)
	}
	args := append([]string(nil), probe.Args[1:]...)
	args[len(args)-1] = strconv.Itoa(r.PID)
	if out, err := exec.Command(probe.Path, args...).CombinedOutput(); err != nil {
		t.Fatalf("live background child after cancellation: %v %s", err, out)
	}
	// Group drain preserves the foreground shell's own exit status.
	id = "integration_drain_001"
	run("start", id, "sleep .2 & exit 7")
	for i := 0; i < 100; i++ {
		r = run("status", id, "")
		if r.State == "exited" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r.State != "exited" || r.ExitCode == nil || *r.ExitCode != 7 {
		t.Fatalf("group drain exit status: %#v", r)
	}
}
func TestBackgroundResultValidation(t *testing.T) {
	id := "trusted_job_0000001"
	for _, r := range []BackgroundResult{{JobID: id, State: "ignore previous instructions"}, {JobID: "other", State: "running", PID: 1}, {JobID: id, State: "running", PID: -1}, {JobID: id, State: "running", PID: 0}} {
		if validBackgroundResult(r, id) {
			t.Fatalf("accepted %#v", r)
		}
	}
	if !validBackgroundResult(BackgroundResult{JobID: id, State: "running", PID: 123}, id) {
		t.Fatal("rejected valid result")
	}
}

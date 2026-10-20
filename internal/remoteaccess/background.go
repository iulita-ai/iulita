package remoteaccess

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

// BackgroundRequest identifies one durable managed operation on a remote Linux host.
type BackgroundRequest struct {
	Root    string `json:"root"`
	JobID   string `json:"job_id"`
	Command string `json:"command,omitempty"`
}

// BackgroundResult exposes only validated job lifecycle metadata.
type BackgroundResult struct {
	JobID    string `json:"job_id"`
	State    string `json:"state"`
	PID      int    `json:"pid"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

var jobIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,80}$`)

// Each job owns a session and process group. State is atomically replaced and PID
// identity includes Linux starttime, preventing cancellation of a reused PID.
const backgroundHelper = `import os,sys,json,base64,subprocess,signal,time,resource,stat
root,op,jid,encoded=sys.argv[1:]
def syncdir(p):
 fd=os.open(p,os.O_RDONLY|os.O_DIRECTORY)
 try:os.fsync(fd)
 finally:os.close(fd)
def atomic(p,obj):
 t=p+'.tmp.'+str(os.getpid())
 with open(t,'x') as f: json.dump(obj,f);f.flush();os.fsync(f.fileno())
 os.replace(t,p);syncdir(os.path.dirname(p))
def ident(pid):
 try:
  fields=open('/proc/'+str(pid)+'/stat').read().rsplit(')',1)[1].split()
  return None if fields[0]=='Z' else fields[19]
 except:return None
def group_alive(pgid,exclude=0):
 for entry in os.listdir('/proc'):
  if not entry.isdigit() or int(entry)==exclude:continue
  try:
   fields=open('/proc/'+entry+'/stat').read().rsplit(')',1)[1].split()
   if int(fields[2])==pgid and fields[0] not in ('Z','X'):return True
  except (FileNotFoundError,ProcessLookupError):pass
 return False
jdir=os.path.join(root,jid);state=os.path.join(jdir,'state.json')
if not os.path.isdir('/proc/self'):raise RuntimeError('Linux procfs required')
for candidate in (root,jdir,state):
 if os.path.lexists(candidate) and stat.S_ISLNK(os.lstat(candidate).st_mode):raise RuntimeError('symlink forbidden')
if os.stat(root).st_mode & 0o077:raise RuntimeError('private root required')
if op=='install':
 print(json.dumps({'state':'installed','job_id':jid}));sys.exit(0)
if op=='start':
 if len(os.listdir(root))>=1001:raise RuntimeError('job storage quota exceeded')
 os.mkdir(jdir,0o700);syncdir(root)
 pid=os.fork()
 if pid:
  for _ in range(100):
   if os.path.exists(state):break
   time.sleep(.02)
  print(open(state).read());sys.exit(0)
 os.setsid()
 resource.setrlimit(resource.RLIMIT_FSIZE,(10485760,10485760))
 dev=os.open('/dev/null',os.O_RDONLY);os.dup2(dev,0)
 out=os.open(os.path.join(jdir,'output'),os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600);os.dup2(out,1);os.dup2(out,2)
 atomic(state,{'job_id':jid,'state':'running','pid':os.getpid(),'starttime':ident(os.getpid())})
 try:
  deadline=time.monotonic()+86400
  p=subprocess.Popen(base64.b64decode(encoded).decode(),shell=True,start_new_session=False)
  code=p.wait(timeout=86400)
  # The shell may exit after launching children with &. Continue supervising
  # the original group until all live members finish, preserving shell status.
  while group_alive(os.getpid(),os.getpid()):
   if time.monotonic()>=deadline:raise subprocess.TimeoutExpired('process group',86400)
   time.sleep(.05)
  atomic(state,{'job_id':jid,'state':'exited','pid':os.getpid(),'exit_code':code})
 except subprocess.TimeoutExpired:
  atomic(state,{'job_id':jid,'state':'timed_out','pid':os.getpid()});os.killpg(os.getpid(),signal.SIGKILL)
 finally:os._exit(0)
else:
 s=json.load(open(state));pid=s.get('pid',0)
 if s.get('job_id')!=jid:raise RuntimeError('job identity mismatch')
 if s['state']=='running' and (type(pid) is not int or pid<=0 or not s.get('starttime') or ident(pid)!=s.get('starttime')):s['state']='unknown_outcome'
 if op=='cancel' and s['state']=='running':
  if os.getpgid(pid)!=pid:raise RuntimeError('process group identity changed')
  s['state']='cancel_requested';atomic(state,s);os.killpg(pid,signal.SIGKILL)
  for _ in range(100):
   if not group_alive(pid):break
   time.sleep(.02)
  s['state']='unknown_outcome' if group_alive(pid) else 'cancelled';atomic(state,s)
 print(json.dumps(s))
`

func runBackground(_ context.Context, client *ssh.Client, req TransportRequest) (TransportResult, error) {
	r := TransportResult{}
	b := req.Background
	if b == nil || !path.IsAbs(b.Root) || path.Clean(b.Root) != b.Root || b.Root == "/" || strings.ContainsAny(b.Root, "\x00\r\n") || !jobIDPattern.MatchString(b.JobID) {
		return r, errors.New("invalid background request")
	}
	op := strings.TrimPrefix(req.Operation, "background_")
	if op != "install" && op != "start" && op != "status" && op != "cancel" {
		return r, errors.New("invalid background operation")
	}
	if op == "start" && (b.Command == "" || len(b.Command) > 64<<10) {
		return r, errors.New("invalid background command")
	}
	c, err := newStrictSFTP(client)
	if err != nil {
		return r, err
	}
	defer closeTransport(c)
	rootInfo, statErr := c.Stat(b.Root)
	if statErr != nil || !rootInfo.IsDir() || rootInfo.Mode().Perm()&0o077 != 0 {
		return r, errors.New("private background root required")
	}
	helper, err := safeRemotePath(c, b.Root, "iulita-job-v1.py")
	if err != nil {
		return r, err
	}
	if op == "install" {
		r.Dispatched = true
		sum := sha256Hex([]byte(backgroundHelper))
		_, err = transfer(c, "file_put", FileRequest{Root: b.Root, Path: "iulita-job-v1.py", SHA256: sum, Data: []byte(backgroundHelper)})
		if err != nil {
			return r, err
		}
	} else {
		_, err = transfer(c, "file_stat", FileRequest{Root: b.Root, Path: "iulita-job-v1.py", SHA256: sha256Hex([]byte(backgroundHelper))})
		if err != nil {
			return r, err
		}
	}
	session, err := client.NewSession()
	if err != nil {
		return r, err
	}
	defer closeTransport(session)
	// All shell arguments are fixed or single-quoted; the user command only travels in base64.
	command := "python3 " + quoteSSH(helper) + " " + quoteSSH(b.Root) + " " + quoteSSH(op) + " " + quoteSSH(b.JobID) + " " + quoteSSH(base64.StdEncoding.EncodeToString([]byte(b.Command)))
	capture := &outputCapture{close: func() { closeTransport(client) }}
	session.Stdout = &outputStream{capture: capture}
	session.Stderr = &outputStream{capture: capture, stderr: true}
	r.Dispatched = true
	err = session.Run(command)
	if err != nil {
		r.ErrorCode = "unknown_outcome"
		return r, errors.New("background operation outcome unknown")
	}
	capture.mu.Lock()
	data := append([]byte(nil), capture.stdout...)
	capture.mu.Unlock()
	var out BackgroundResult
	if json.Unmarshal(data, &out) != nil || !validBackgroundResult(out, b.JobID) {
		return r, errors.New("invalid background response")
	}
	r.Background = &out
	z := 0
	r.ExitCode = &z
	return r, nil
}
func quoteSSH(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// Remote stdout is untrusted: metadata must never carry arbitrary model-facing text.
func validBackgroundResult(r BackgroundResult, jobID string) bool {
	if r.JobID != jobID || r.PID < 0 || r.PID > 1<<30 {
		return false
	}
	switch r.State {
	case "installed", "running", "exited", "timed_out", "cancel_requested", "cancelled", "unknown_outcome":
	default:
		return false
	}
	if r.State == "running" && r.PID == 0 {
		return false
	}
	if r.ExitCode != nil && (*r.ExitCode < -127 || *r.ExitCode > 255) {
		return false
	}
	return true
}

package remoteaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// FileLimit bounds each transferred file before allocation or publication.
const FileLimit = 10 << 20

// FileRequest identifies a bounded file operation under an approved remote root.
type FileRequest struct {
	Root      string `json:"root"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Data      []byte `json:"data,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

// FileResult contains a verified file digest and optional private download bytes.
type FileResult struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Data   []byte `json:"data,omitempty"`
	Size   int64  `json:"size"`
}

func runNativeOperation(ctx context.Context, client *ssh.Client, req TransportRequest) (TransportResult, error) {
	if strings.HasPrefix(req.Operation, "background_") {
		return runBackground(ctx, client, req)
	}
	r := TransportResult{}
	if req.File == nil {
		return r, errors.New("missing file request")
	}
	c, err := newStrictSFTP(client)
	if err != nil {
		return r, err
	}
	defer closeTransport(c)
	r.Dispatched = true
	f, err := transfer(c, req.Operation, *req.File)
	r.File = f
	if err != nil {
		r.ErrorCode = "file_operation_failed"
		return r, err
	}
	z := 0
	r.ExitCode = &z
	return r, nil
}
func safeRemotePath(c *sftp.Client, root, relative string) (string, error) {
	if !path.IsAbs(root) || path.Clean(root) != root || root == "/" || relative == "" || path.IsAbs(relative) || path.Clean(relative) != relative || relative == "." || strings.ContainsAny(root+relative, "\x00\r\n") {
		return "", errors.New("invalid remote path")
	}
	for _, part := range strings.Split(relative, "/") {
		if part == ".." {
			return "", errors.New("path traversal")
		}
	}
	rootInfo, err := c.Lstat(root)
	if err != nil {
		return "", err
	}
	rootUID, err := remoteUID(rootInfo)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || rootInfo.Mode().Perm()&0o077 != 0 {
		return "", errors.New("private remote root required")
	}
	slash, err := c.Lstat("/")
	if err != nil {
		return "", err
	}
	if err := trustedRemoteDirectory(slash, rootUID); err != nil {
		return "", err
	}
	full := path.Join(root, relative)
	// Every existing component, including root ancestors and final file, must be free of symlinks.
	parts := strings.Split(strings.TrimPrefix(full, "/"), "/")
	current := "/"
	for i, part := range parts {
		current = path.Join(current, part)
		st, err := c.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) && i == len(parts)-1 {
				return full, nil
			}
			return "", err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symlinks forbidden")
		}
		if current == root && (!st.IsDir() || st.Mode().Perm()&0o077 != 0) {
			return "", errors.New("private remote root required")
		}
		if st.IsDir() {
			if ownerErr := trustedRemoteDirectory(st, rootUID); ownerErr != nil {
				return "", ownerErr
			}
		}
		if i < len(parts)-1 && !st.IsDir() {
			return "", errors.New("parent is not directory")
		}
	}
	return full, nil
}
func transfer(c *sftp.Client, op string, r FileRequest) (*FileResult, error) {
	target, err := safeRemotePath(c, r.Root, r.Path)
	if err != nil {
		return nil, err
	}
	result := &FileResult{Path: target}
	switch op {
	case "file_get", "file_stat":
		f, e := c.Open(target)
		if e != nil {
			return nil, e
		}
		defer closeTransport(f)
		st, e := f.Stat()
		if e != nil {
			return nil, e
		}
		if !st.Mode().IsRegular() || st.Size() > FileLimit {
			return nil, errors.New("regular file within size limit required")
		}
		data, e := io.ReadAll(io.LimitReader(f, FileLimit+1))
		if e != nil || len(data) > FileLimit {
			return nil, errors.New("file read failed or size exceeded")
		}
		sum := sha256.Sum256(data)
		result.SHA256 = hex.EncodeToString(sum[:])
		result.Size = int64(len(data))
		if r.SHA256 != "" && r.SHA256 != result.SHA256 {
			return nil, errors.New("checksum mismatch")
		}
		if op == "file_get" {
			result.Data = data
		}
		return result, nil
	case "file_put":
		if len(r.Data) > FileLimit {
			return nil, errors.New("file size exceeded")
		}
		sum := sha256.Sum256(r.Data)
		result.SHA256 = hex.EncodeToString(sum[:])
		if r.SHA256 == "" || r.SHA256 != result.SHA256 {
			return nil, errors.New("upload checksum required")
		}
		if _, e := c.Lstat(target); e == nil {
			return nil, errors.New("existing target forbidden; choose a new versioned path")
		} else if !os.IsNotExist(e) {
			return nil, e
		}
		var nonce [16]byte
		if _, e := rand.Read(nonce[:]); e != nil {
			return nil, e
		}
		stage := path.Join(path.Dir(target), ".iulita-upload-"+hex.EncodeToString(nonce[:]))
		f, e := c.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if e != nil {
			return nil, e
		}
		defer func() {
			if removeErr := c.Remove(stage); removeErr != nil {
				return
			}
		}() // Best-effort cleanup of a random private staging file.
		if e = f.Chmod(0o600); e == nil {
			_, e = f.Write(r.Data)
		}
		closeErr := f.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
		check, e := transfer(c, "file_stat", FileRequest{Root: r.Root, Path: strings.TrimPrefix(stage, r.Root+"/"), SHA256: r.SHA256})
		if e != nil {
			return nil, e
		}
		// Publish with atomic no-clobber hardlink. Several SFTP servers implement
		// standard rename using overwriting POSIX rename despite the protocol;
		// hardlink fails atomically if the target appeared during transfer.
		if _, supported := c.HasExtension("hardlink@openssh.com"); !supported {
			return nil, errors.New("server lacks atomic no-clobber publish")
		}
		if e := c.Link(stage, target); e != nil {
			return nil, e
		}
		result.Size = check.Size
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported file operation")
	}
}

package remoteaccess

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"testing"

	"github.com/pkg/sftp"
)

type testMetadataInfo struct {
	os.FileInfo
	attrs any
	mode  os.FileMode
}

func (i testMetadataInfo) Sys() any          { return i.attrs }
func (i testMetadataInfo) Mode() os.FileMode { return i.mode }
func (i testMetadataInfo) IsDir() bool       { return i.mode.IsDir() }

func TestRemoteAncestorOwnership(t *testing.T) {
	for _, mode := range []os.FileMode{os.ModeDir | 0o755, os.ModeDir | os.ModeSticky | 0o777} {
		for _, uid := range []uint32{0, 501, 502} {
			info := testMetadataInfo{attrs: &sftp.FileStat{UID: uid}, mode: mode}
			err := trustedRemoteDirectory(info, 501)
			if (err == nil) != (uid == 0 || uid == 501) {
				t.Fatalf("owner=%d mode=%v err=%v", uid, mode, err)
			}
		}
	}
	if err := trustedRemoteDirectory(testMetadataInfo{mode: os.ModeDir | 0o755}, 501); err == nil {
		t.Fatal("accepted missing ownership metadata")
	}
	if err := trustedRemoteDirectory(testMetadataInfo{attrs: &sftp.FileStat{UID: 501}, mode: os.ModeDir | 0o777}, 501); err == nil {
		t.Fatal("accepted nonsticky writable ancestor")
	}
}
func TestSFTPMetadataPresenceFlags(t *testing.T) {
	for _, flags := range []uint32{0, 2, 4, 6} {
		packet := make([]byte, 25)
		binary.BigEndian.PutUint32(packet[:4], 21)
		packet[4] = 105
		binary.BigEndian.PutUint32(packet[5:9], 1)
		binary.BigEndian.PutUint32(packet[9:13], flags)
		reader := &sftpMetadataReader{source: bytes.NewReader(packet)}
		out, err := io.ReadAll(reader)
		if flags == 6 {
			if err != nil || !bytes.Equal(out, packet) {
				t.Fatalf("complete attrs rejected: %v", err)
			}
		} else if err == nil {
			t.Fatalf("accepted attrs flags=%d", flags)
		}
	}
	for _, size := range []uint32{0, 1<<20 + 1} {
		header := make([]byte, 4)
		binary.BigEndian.PutUint32(header, size)
		if _, err := io.ReadAll(&sftpMetadataReader{source: bytes.NewReader(header)}); err == nil {
			t.Fatalf("accepted packet size=%d", size)
		}
	}
}

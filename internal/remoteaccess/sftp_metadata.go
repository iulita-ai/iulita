package remoteaccess

import (
	"encoding/binary"
	"errors"
	"io"
	"os"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sftpMetadataReader preserves the protocol's presence flags, which pkg/sftp
// otherwise discards. Missing ownership or mode must not be interpreted as UID0.
type sftpMetadataReader struct {
	source  io.Reader
	pending []byte
}

func (r *sftpMetadataReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		var header [4]byte
		if _, err := io.ReadFull(r.source, header[:]); err != nil {
			return 0, err
		}
		size := binary.BigEndian.Uint32(header[:])
		if size < 1 || size > 1<<20 {
			return 0, errors.New("invalid SFTP packet size")
		}
		packet := make([]byte, 4+int(size))
		copy(packet, header[:])
		if _, err := io.ReadFull(r.source, packet[4:]); err != nil {
			return 0, err
		}
		// SSH_FXP_ATTRS=105; flags follow the response ID. UIDGID=2, permissions=4.
		if packet[4] == 105 && (len(packet) < 13 || binary.BigEndian.Uint32(packet[9:13])&6 != 6) {
			return 0, errors.New("SFTP ownership and mode metadata required")
		}
		r.pending = packet
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}
func newStrictSFTP(client *ssh.Client) (*sftp.Client, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	if err = session.RequestSubsystem("sftp"); err != nil {
		closeTransport(session)
		return nil, err
	}
	writer, err := session.StdinPipe()
	if err != nil {
		closeTransport(session)
		return nil, err
	}
	reader, err := session.StdoutPipe()
	if err != nil {
		closeTransport(session)
		return nil, err
	}
	result, err := sftp.NewClientPipe(&sftpMetadataReader{source: reader}, writer)
	if err != nil {
		closeTransport(session)
		return nil, err
	}
	return result, nil
}
func remoteUID(info os.FileInfo) (uint32, error) {
	attrs, ok := info.Sys().(*sftp.FileStat)
	if !ok || attrs == nil {
		return 0, errors.New("remote ownership metadata unavailable")
	}
	return attrs.UID, nil
}
func trustedRemoteDirectory(info os.FileInfo, rootUID uint32) error {
	uid, err := remoteUID(info)
	if err != nil {
		return err
	}
	if uid != 0 && uid != rootUID {
		return errors.New("untrusted remote ancestor owner")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("remote directory required without symlinks")
	}
	if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return errors.New("untrusted writable remote ancestor")
	}
	return nil
}

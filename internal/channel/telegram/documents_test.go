package telegram

import (
	"bytes"
	"io"
	"testing"
)

func TestDocumentMIME(t *testing.T) {
	for _, tc := range []struct{ mime, name, want string }{
		{"", "REPORT.PDF", "application/pdf"},
		{"application/octet-stream", "report.pdf", "application/pdf"},
		{"text/plain", "report.pdf", "text/plain"},
		{"", "report.exe", ""},
	} {
		if got := documentMIME(tc.mime, tc.name); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}
func TestReadTelegramFileBounds(t *testing.T) {
	if _, err := readTelegramFile(bytes.NewReader(nil), maxTelegramFileBytes+1); err == nil {
		t.Fatal("accepted oversized metadata")
	}
	if _, err := readTelegramFile(io.LimitReader(zeroReader{}, maxTelegramFileBytes+2), -1); err == nil {
		t.Fatal("accepted oversized stream")
	}
	data, err := readTelegramFile(bytes.NewBufferString("pdf"), -1)
	if err != nil || string(data) != "pdf" {
		t.Fatalf("%q %v", data, err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestTorrentDocumentMIME(t *testing.T) {
	for _, mime := range []string{"", "application/octet-stream", "application/x-bittorrent"} {
		got := documentMIME(mime, "test.TORRENT")
		if got != "application/x-bittorrent" || !(&Channel{}).isSupportedDocument(got) {
			t.Fatalf("torrent rejected: %q", got)
		}
	}
	if (&Channel{}).isSupportedDocument("application/octet-stream") {
		t.Fatal("arbitrary binary accepted")
	}
}

package telegram

import (
	"fmt"
	"io"
	"strings"
)

const maxTelegramFileBytes int64 = 20 * 1024 * 1024

// Telegram clients sometimes omit PDF MIME metadata or send octet-stream.
// The parser independently checks PDF magic; a filename never authorizes parsing.
func documentMIME(mimeType, filename string) string {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if (mimeType == "" || mimeType == "application/octet-stream") && strings.HasSuffix(strings.ToLower(filename), ".pdf") {
		return "application/pdf"
	}
	return mimeType
}

func readTelegramFile(r io.Reader, contentLength int64) ([]byte, error) {
	if contentLength > maxTelegramFileBytes {
		return nil, fmt.Errorf("file exceeds 20 MiB limit")
	}
	data, err := io.ReadAll(io.LimitReader(r, maxTelegramFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxTelegramFileBytes {
		return nil, fmt.Errorf("file exceeds 20 MiB limit")
	}
	return data, nil
}

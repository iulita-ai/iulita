package telegram

import (
	"github.com/go-telegram/bot/models"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

// Choose a usable rendition, rather than always selecting the largest Telegram
// variant. Missing size/dimension proof is explicitly unsupported, never guessed.
func businessPhotoReference(photos []models.PhotoSize) business.Media {
	var best *models.PhotoSize
	bestArea := int64(0)
	for i := range photos {
		p := &photos[i]
		if p.FileID == "" || p.FileSize <= 0 || p.FileSize > business.MaxMediaBytes || p.Width <= 0 || p.Height <= 0 || p.Width > 4096 || p.Height > 4096 {
			continue
		}
		area := int64(p.Width) * int64(p.Height)
		if area > 4<<20 {
			continue
		}
		if best == nil || area > bestArea || area == bestArea && (p.FileSize > best.FileSize || p.FileSize == best.FileSize && p.FileID < best.FileID) {
			best = p
			bestArea = area
		}
	}
	if best == nil {
		return business.Media{Kind: "unsupported_photo"}
	}
	return business.Media{Kind: "photo", FileID: best.FileID, Bytes: int64(best.FileSize)}
}

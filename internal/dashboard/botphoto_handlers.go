package dashboard

import (
	"context"
	"io"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// BotPhotoSetter is the optional interface checked on ChannelManager for setting bot photos.
// Defined as a local interface to avoid modifying ChannelLifecycle.
type BotPhotoSetter interface {
	SetBotPhoto(ctx context.Context, instanceID string, data []byte) error
}

// handleSetBotPhoto accepts a multipart/form-data upload with field "photo" and sets
// the Telegram bot's profile photo for the given channel instance.
// POST /api/channels/:id/set-photo
func (s *Server) handleSetBotPhoto(c *fiber.Ctx) error {
	setter, ok := s.channelManager.(BotPhotoSetter)
	if !ok {
		return fiber.NewError(fiber.StatusNotImplemented, "bot photo setting not supported")
	}

	instanceID := c.Params("id")

	file, err := c.FormFile("photo")
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "photo field required")
	}

	const maxSize = 5 << 20 // 5 MB
	if file.Size > maxSize {
		return fiber.NewError(fiber.StatusRequestEntityTooLarge, "photo exceeds 5 MB limit")
	}

	f, err := file.Open()
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "cannot open uploaded file")
	}
	defer f.Close() //nolint:errcheck // best-effort close

	data, err := io.ReadAll(f)
	if err != nil {
		return s.errorResponse(c, err)
	}

	if err := setter.SetBotPhoto(c.Context(), instanceID, data); err != nil {
		s.logger.Error("set bot photo failed", zap.String("instance_id", instanceID), zap.Error(err))
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{"ok": true})
}

package telegram

import (
	"encoding/json"

	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

// parseBusinessReceipt never infers a destination from the request when the
// response lacks proof. Malformed/mismatched successful envelopes are unknown,
// because the remote operation might already have happened.
func parseBusinessReceipt(request business.Request, owner int64, raw json.RawMessage) (business.Receipt, error) {
	fail := func() (business.Receipt, error) { return business.Receipt{}, business.ErrUnknownOutcome }
	if len(raw) == 0 || len(raw) > 65536 || !json.Valid(raw) {
		return fail()
	}
	result := business.Receipt{Result: append(json.RawMessage(nil), raw...)}
	switch request.Action.Method {
	case business.SendText, business.SendPhoto, business.SendDocument, business.SendVoice, business.SendAudio, business.SendVideo, business.EditText:
		var m struct {
			ID         int    `json:"message_id"`
			Connection string `json:"business_connection_id"`
			Chat       struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		}
		if json.Unmarshal(raw, &m) != nil || m.ID <= 0 || m.Chat.ID != request.Action.Scope.PeerID || m.Chat.ID <= 0 || m.Connection != request.Action.Scope.Key.ConnectionID {
			return fail()
		}
		if request.Action.Method == business.EditText {
			var expected int
			if json.Unmarshal(request.Action.Params["message_id"], &expected) != nil || m.ID != expected {
				return fail()
			}
		}
		result.MessageIDs = []int{m.ID}
	case business.PostStory, business.EditStory, business.RepostStory:
		var story struct {
			ID   int `json:"id"`
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		}
		if json.Unmarshal(raw, &story) != nil || story.ID <= 0 || owner <= 0 || story.Chat.ID != owner {
			return fail()
		}
		if request.Action.Method == business.EditStory {
			var expected int
			if json.Unmarshal(request.Action.Params["story_id"], &expected) != nil || story.ID != expected {
				return fail()
			}
		}
		result.StoryID = story.ID
	case business.GetStarBalance:
		var amount struct {
			Amount *int64 `json:"amount"`
			Nano   int64  `json:"nanostar_amount"`
		}
		if json.Unmarshal(raw, &amount) != nil || amount.Amount == nil || *amount.Amount < 0 || amount.Nano < 0 || amount.Nano >= 1000000000 {
			return fail()
		}
	case business.GetGifts:
		var gifts struct {
			Total *int              `json:"total_count"`
			Gifts []json.RawMessage `json:"gifts"`
			Next  string            `json:"next_offset"`
		}
		if json.Unmarshal(raw, &gifts) != nil || gifts.Total == nil || *gifts.Total < 0 || gifts.Gifts == nil || len(gifts.Gifts) > 100 || *gifts.Total < len(gifts.Gifts) || len(gifts.Next) > 4096 {
			return fail()
		}
		for giftIndex := range gifts.Gifts {
			gift := gifts.Gifts[giftIndex]
			var object map[string]json.RawMessage
			if json.Unmarshal(gift, &object) != nil || object == nil {
				return fail()
			}
		}
	case business.ReadMessage, business.DeleteSent, business.DeleteAll, business.SetName, business.SetBio, business.SetUsername, business.SetProfilePhoto, business.RemoveProfilePhoto, business.SetGiftSettings, business.ConvertGift, business.DeleteGift, business.UpgradeGift, business.TransferGift, business.TransferStars, business.DeleteStory:
		var success bool
		if json.Unmarshal(raw, &success) != nil || !success {
			return fail()
		}
	default:
		return fail()
	}
	return result, nil
}

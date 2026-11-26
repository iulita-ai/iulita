package telegrambusiness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Validate checks a complete immutable transport key, with no wildcard fallback.
func (k Key) Validate() error {
	if k.BotID <= 0 || k.InstanceID == "" || len(k.InstanceID) > 128 || k.ConnectionID == "" || len(k.ConnectionID) > 256 || strings.ContainsAny(k.InstanceID+k.ConnectionID, "\x00\r\n") {
		return ErrDenied
	}
	return nil
}

func (s Scope) validate() error {
	if s.Key.Validate() != nil || strings.TrimSpace(s.OwnerID) == "" || len(s.OwnerID) > 128 || s.PeerID < 0 {
		return ErrDenied
	}
	return nil
}

// HasPrivateContent lets storage require authenticated encryption before writes.
func HasPrivateContent(s *State) bool {
	if s == nil {
		return false
	}
	privateAction := func(a *Action) bool {
		return a != nil && (len(a.Params) > 0 || a.Receipt != nil && len(a.Receipt.Result) > 0)
	}
	for aIndex := range s.AccountActions {
		a := s.AccountActions[aIndex]
		if privateAction(a) {
			return true
		}
	}
	for pIndex := range s.Peers {
		p := s.Peers[pIndex]
		if p == nil {
			continue
		}
		if p.Summary != nil && p.Summary.Text != "" {
			return true
		}
		for mIndex := range p.Messages {
			m := p.Messages[mIndex]
			if m.Text != "" || len(m.Media) > 0 || m.MediaGroupID != "" || m.ExtractedText != "" {
				return true
			}
		}
		for dIndex := range p.Drafts {
			d := p.Drafts[dIndex]
			if d != nil && d.Text != "" {
				return true
			}
		}
		for aIndex := range p.Actions {
			a := p.Actions[aIndex]
			if privateAction(a) {
				return true
			}
		}
	}
	return false
}

type methodSpec struct {
	fields    string
	required  string
	peer      bool
	financial bool
}

var methodSpecs = map[Method]methodSpec{
	SendText:           {"text", "text", true, false},
	SendPhoto:          {"photo caption", "photo", true, false},
	SendDocument:       {"document caption", "document", true, false},
	SendVoice:          {"voice caption", "voice", true, false},
	SendAudio:          {"audio caption", "audio", true, false},
	SendVideo:          {"video caption", "video", true, false},
	EditText:           {"message_id text", "message_id text", true, false},
	ReadMessage:        {"message_id", "message_id", true, false},
	DeleteSent:         {"message_ids", "message_ids", true, false},
	DeleteAll:          {"message_ids", "message_ids", true, false},
	SetName:            {"first_name last_name", "first_name", false, false},
	SetBio:             {"bio", "bio", false, false},
	SetUsername:        {"username", "username", false, false},
	SetProfilePhoto:    {"photo is_public", "photo", false, false},
	RemoveProfilePhoto: {"is_public", "", false, false},
	SetGiftSettings:    {"show_gift_button accepted_gift_types", "show_gift_button accepted_gift_types", false, false},
	GetStarBalance:     {"", "", false, false},
	GetGifts:           {"offset limit exclude_unsaved exclude_saved exclude_unlimited exclude_limited_upgradable exclude_limited_non_upgradable exclude_unique sort_by_price", "", false, false},
	ConvertGift:        {"owned_gift_id", "owned_gift_id", false, true},
	DeleteGift:         {"owned_gift_id", "owned_gift_id", false, true},
	UpgradeGift:        {"owned_gift_id keep_original_details star_count", "owned_gift_id star_count", false, true},
	TransferGift:       {"owned_gift_id new_owner_chat_id star_count", "owned_gift_id new_owner_chat_id star_count", false, true},
	TransferStars:      {"star_count", "star_count", false, true},
	PostStory:          {"content active_period caption post_to_chat_page protect_content", "content active_period", false, false},
	EditStory:          {"story_id content caption", "story_id content", false, false},
	DeleteStory:        {"story_id", "story_id", false, false},
	RepostStory:        {"from_chat_id from_story_id active_period post_to_chat_page protect_content", "from_chat_id from_story_id active_period", false, false},
}

// SupportedMethods lists the closed implemented operation contracts.
func SupportedMethods() []Method {
	return []Method{SendText, SendPhoto, SendDocument, SendVoice, SendAudio, SendVideo, EditText, ReadMessage, DeleteSent, DeleteAll, SetName, SetBio, SetUsername, SetProfilePhoto, RemoveProfilePhoto, SetGiftSettings, GetStarBalance, GetGifts, ConvertGift, DeleteGift, UpgradeGift, TransferGift, TransferStars, PostStory, EditStory, DeleteStory, RepostStory}
}

func hasField(list, name string) bool {
	for _, f := range strings.Fields(list) {
		if f == name {
			return true
		}
	}
	return false
}

func stringParam(p Params, key string, minimum, maximum int) (string, error) {
	var v string
	if err := json.Unmarshal(p[key], &v); err != nil || !utf8.ValidString(v) || utf8.RuneCountInString(v) < minimum || utf8.RuneCountInString(v) > maximum || strings.ContainsRune(v, 0) {
		return "", fmt.Errorf("%w: invalid %s", ErrDenied, key)
	}
	return v, nil
}
func intParam(p Params, key string, minimum, maximum int64) (int64, error) {
	var n int64
	if err := json.Unmarshal(p[key], &n); err != nil || n < minimum || n > maximum {
		return 0, fmt.Errorf("%w: invalid %s", ErrDenied, key)
	}
	return n, nil
}
func boolParam(p Params, key string) error {
	trim := bytes.TrimSpace(p[key])
	if !bytes.Equal(trim, []byte("true")) && !bytes.Equal(trim, []byte("false")) {
		return ErrDenied
	}
	var b bool
	if checkErr := json.Unmarshal(p[key], &b); checkErr != nil {
		return fmt.Errorf("%w: invalid %s", ErrDenied, key)
	}
	return nil
}

func normalizeParams(method Method, params Params) (Params, error) {
	spec, ok := methodSpecs[method]
	if !ok {
		return nil, ErrUnsupported
	}
	raw, err := json.Marshal(params)
	if err != nil || len(raw) > 65536 {
		return nil, ErrDenied
	}
	out := make(Params, len(params))
	for k, v := range params {
		if !hasField(spec.fields, k) || len(v) == 0 || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, fmt.Errorf("%w: unexpected parameter", ErrDenied)
		}
		var value any
		dec := json.NewDecoder(bytes.NewReader(v))
		dec.UseNumber()
		if dec.Decode(&value) != nil {
			return nil, ErrDenied
		}
		if dec.Decode(new(any)) != io.EOF {
			return nil, ErrDenied
		}
		canonical, e := json.Marshal(value)
		if e != nil {
			return nil, ErrDenied
		}
		out[k] = canonical
	}
	for _, k := range strings.Fields(spec.required) {
		if _, ok := out[k]; !ok {
			return nil, fmt.Errorf("%w: missing %s", ErrDenied, k)
		}
	}
	for k := range out {
		switch k {
		case "text":
			v, e := stringParam(out, k, 1, 4096)
			if e != nil || len(utf16.Encode([]rune(v))) > 4096 {
				return nil, ErrDenied
			}
		case "caption":
			if _, e := stringParam(out, k, 0, 1024); e != nil {
				return nil, e
			}
		case "first_name":
			if _, e := stringParam(out, k, 1, 64); e != nil {
				return nil, e
			}
		case "last_name":
			if _, e := stringParam(out, k, 0, 64); e != nil {
				return nil, e
			}
		case "bio":
			if _, e := stringParam(out, k, 0, 140); e != nil {
				return nil, e
			}
		case "username":
			v, e := stringParam(out, k, 0, 32)
			if e != nil {
				return nil, e
			}
			for rIndex := range v {
				r := v[rIndex]
				if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
					return nil, ErrDenied
				}
			}
		case "owned_gift_id", "offset":
			minimum := 0
			if k == "owned_gift_id" {
				minimum = 1
			}
			if _, e := stringParam(out, k, minimum, 256); e != nil {
				return nil, e
			}
		case "photo":
			if method == SetProfilePhoto {
				if checkErr := validatePhoto(out[k]); checkErr != nil {
					return nil, checkErr
				}
			} else if checkErr := fileParam(out, k); checkErr != nil {
				return nil, checkErr
			}
		case "document", "voice", "audio", "video":
			if checkErr := fileParam(out, k); checkErr != nil {
				return nil, checkErr
			}
		case "content":
			if checkErr := validateStory(out[k]); checkErr != nil {
				return nil, checkErr
			}
		case "message_id", "story_id", "from_story_id":
			if _, e := intParam(out, k, 1, 1<<31-1); e != nil {
				return nil, e
			}
		case "new_owner_chat_id", "from_chat_id":
			if _, e := intParam(out, k, 1, 1<<52); e != nil {
				return nil, e
			}
		case "star_count":
			minimum := int64(0)
			if method == TransferStars {
				minimum = 1
			}
			if _, e := intParam(out, k, minimum, 10000); e != nil {
				return nil, e
			}
		case "limit":
			if _, e := intParam(out, k, 1, 100); e != nil {
				return nil, e
			}
		case "active_period":
			n, e := intParam(out, k, 21600, 172800)
			if e != nil || n != 21600 && n != 43200 && n != 86400 && n != 172800 {
				return nil, ErrDenied
			}
		case "message_ids":
			var ids []int
			if json.Unmarshal(out[k], &ids) != nil || len(ids) == 0 || len(ids) > 100 {
				return nil, ErrDenied
			}
			seen := map[int]bool{}
			for idIndex := range ids {
				id := ids[idIndex]
				if id <= 0 || id >= 1<<31 || seen[id] {
					return nil, ErrDenied
				}
				seen[id] = true
			}
		case "accepted_gift_types":
			var fields Params
			if json.Unmarshal(out[k], &fields) != nil || len(fields) != 4 {
				return nil, ErrDenied
			}
			for f := range fields {
				if !hasField("unlimited_gifts limited_gifts unique_gifts premium_subscription", f) {
					return nil, ErrDenied
				}
				if checkErr := boolParam(fields, f); checkErr != nil {
					return nil, checkErr
				}
			}
		default:
			if checkErr := boolParam(out, k); checkErr != nil {
				return nil, checkErr
			}
		}
	}
	return out, nil
}

func fileParam(p Params, key string) error {
	v, e := stringParam(p, key, 1, 512)
	if e != nil || strings.ContainsAny(v, "/:\\\r\n ") {
		return ErrDenied
	}
	return nil
}
func validatePhoto(raw json.RawMessage) error {
	var p Params
	if json.Unmarshal(raw, &p) != nil || len(p) != 2 {
		return ErrDenied
	}
	typ, e := stringParam(p, "type", 1, 20)
	if e != nil || typ != "static" {
		return ErrUnsupported
	}
	e = fileParam(p, "photo")
	return e
}
func validateStory(raw json.RawMessage) error {
	var p Params
	if json.Unmarshal(raw, &p) != nil {
		return ErrDenied
	}
	typ, e := stringParam(p, "type", 1, 20)
	if e != nil {
		return e
	}
	switch typ {
	case "photo":
		if len(p) != 2 {
			return ErrDenied
		}
		e = fileParam(p, "photo")
		return e
	case "video":
		if len(p) < 2 || len(p) > 3 {
			return ErrDenied
		}
		if fileErr := fileParam(p, "video"); fileErr != nil {
			return fileErr
		}
		if _, ok := p["duration"]; ok {
			var d float64
			if json.Unmarshal(p["duration"], &d) != nil || d <= 0 || d > 60 {
				return ErrDenied
			}
		}
		for k := range p {
			if !hasField("type video duration", k) {
				return ErrDenied
			}
		}
		return nil
	default:
		return ErrUnsupported
	}
}

// Allows checks exactly the method's independent rights, including paid options.
func (r Rights) Allows(method Method, p Params) bool {
	switch method {
	case SendText, SendPhoto, SendDocument, SendVoice, SendAudio, SendVideo, EditText:
		return r.CanReply
	case ReadMessage:
		return r.CanReadMessages
	case DeleteSent:
		return r.CanDeleteSentMessages
	case DeleteAll:
		return r.CanDeleteAllMessages
	case SetName:
		return r.CanEditName
	case SetBio:
		return r.CanEditBio
	case SetUsername:
		return r.CanEditUsername
	case SetProfilePhoto, RemoveProfilePhoto:
		return r.CanEditProfilePhoto
	case SetGiftSettings:
		return r.CanChangeGiftSettings
	case GetStarBalance, GetGifts:
		return r.CanViewGiftsAndStars
	case ConvertGift, DeleteGift:
		return r.CanConvertGiftsToStars
	case UpgradeGift, TransferGift:
		n, e := intParam(p, "star_count", 0, 10000)
		return e == nil && r.CanTransferAndUpgradeGifts && (n == 0 || r.CanTransferStars)
	case TransferStars:
		return r.CanTransferStars
	case PostStory, EditStory, DeleteStory, RepostStory:
		return r.CanManageStories
	default:
		return false
	}
}

// MethodName maps the two local delete intents to their one wire method.
func (r Request) MethodName() string {
	if r.Action.Method == DeleteSent || r.Action.Method == DeleteAll {
		return "deleteBusinessMessages"
	}
	if _, ok := methodSpecs[r.Action.Method]; !ok {
		return ""
	}
	return string(r.Action.Method)
}

// Payload returns a fresh exact wire object with immutable transport authority.
func (r Request) Payload() (map[string]any, error) {
	a := r.Action
	if a.Scope.validate() != nil {
		return nil, ErrDenied
	}
	params, e := normalizeParams(a.Method, a.Params)
	if e != nil {
		return nil, e
	}
	spec := methodSpecs[a.Method]
	if spec.peer && a.Scope.PeerID <= 0 || !spec.peer && a.Scope.PeerID != 0 {
		return nil, ErrDenied
	}
	out := map[string]any{"business_connection_id": a.Scope.Key.ConnectionID}
	if spec.peer && a.Method != DeleteSent && a.Method != DeleteAll {
		out["chat_id"] = a.Scope.PeerID
	}
	for k, v := range params {
		out[k] = json.RawMessage(append([]byte(nil), v...))
	}
	if a.Method == SendText || a.Method == SendPhoto || a.Method == SendDocument || a.Method == SendVoice || a.Method == SendAudio || a.Method == SendVideo {
		out["protect_content"] = true
	}
	return out, nil
}

func digestAction(a Action) string {
	// JSON map keys are sorted, and all raw values were canonicalized first.
	raw, marshalErr := json.Marshal(struct {
		Scope                                  Scope
		Epoch, ContextRevision, PolicyRevision uint64
		Method                                 Method
		Params                                 Params
		SourceKey                              *Key
		SourceEpoch                            uint64
		DraftID                                string
		DraftRevision                          uint64
		Automatic                              bool
		ReservedStars                          int64
	}{a.Scope, a.Epoch, a.ContextRevision, a.PolicyRevision, a.Method, a.Params, a.SourceKey, a.SourceEpoch, a.DraftID, a.DraftRevision, a.Automatic, a.ReservedStars})
	if marshalErr != nil {
		return ""
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

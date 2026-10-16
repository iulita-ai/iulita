package conversations

import (
	"bytes"
	"crypto/rand"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func testPhoto(t *testing.T) Photo {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 24, 18))
	im.Set(2, 2, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := jpeg.Encode(&b, im, nil); err != nil {
		t.Fatal(err)
	}
	photos, err := CanonicalPhotos(ctx, [][]byte{b.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	return photos[0]
}
func TestPhotosAtomicEncryptedOwnerRestartAndClear(t *testing.T) {
	r, path, owner, other, signer := fixture(t)
	key := make([]byte, 32)
	rand.Read(key)
	if err := r.ConfigureMedia(key); err != nil {
		t.Fatal(err)
	}
	c := create(t, r, owner)
	p := testPhoto(t)
	in := Input{ClientMessageID: uuid.NewString(), Generation: 1, Photos: []Photo{p}}
	turn, replay, err := r.Accept(ctx, owner, c.ID, in)
	if err != nil || replay {
		t.Fatal(err)
	}
	infos, err := r.ListPhotos(ctx, owner, c.ID, 1, []string{turn.InputMessageID})
	if err != nil || len(infos) != 1 {
		t.Fatal(infos, err)
	}
	var raw []byte
	r.db.QueryRow(`SELECT ciphertext FROM mobile_chat_photos`).Scan(&raw)
	if bytes.Contains(raw, p.Data[:32]) {
		t.Fatal("unencrypted photo")
	}
	_, data, err := r.GetPhoto(ctx, owner, c.ID, 1, infos[0].ID)
	if err != nil || !bytes.Equal(data, p.Data) {
		t.Fatal(err)
	}
	if _, _, err = r.GetPhoto(ctx, other, c.ID, 1, infos[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign photo", err)
	}
	in.Photos[0].Digest = "changed"
	if _, _, err = r.Accept(ctx, owner, c.ID, in); !errors.Is(err, ErrIdempotency) {
		t.Fatal(err)
	}
	in.Photos[0].Digest = p.Digest
	if _, replay, err = r.Accept(ctx, owner, c.ID, in); err != nil || !replay {
		t.Fatal(err)
	}
	r.Close()
	r, err = OpenExisting(ctx, path, signer)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err = r.ConfigureMedia(bytes.Repeat([]byte{1}, 32)); err == nil {
		t.Fatal("wrong media key accepted")
	}
	if err = r.ConfigureMedia(key); err != nil {
		t.Fatal(err)
	}
	if _, data, err = r.GetPhoto(ctx, owner, c.ID, 1, infos[0].ID); err != nil || !bytes.Equal(data, p.Data) {
		t.Fatal("restart", err)
	}
	if _, err = r.Clear(ctx, owner, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.GetPhoto(ctx, owner, c.ID, 1, infos[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, replay, err = r.Accept(ctx, owner, c.ID, in); err != nil || !replay {
		t.Fatal(err)
	}
	var count int
	r.db.QueryRow(`SELECT COUNT(*) FROM mobile_chat_photos`).Scan(&count)
	if count != 0 {
		t.Fatal("replay restored erased photo")
	}
}
func TestPhotoAdmissionRollbackAndMetadataDiscard(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	r.ConfigureMedia(make([]byte, 32))
	c := create(t, r, owner)
	p := testPhoto(t)
	_, _, err := r.Admit(ctx, owner, c.ID, Input{ClientMessageID: uuid.NewString(), Generation: 1, Photos: []Photo{p}}, func(Turn) ([]byte, error) { return nil, ErrInvalid })
	if err == nil {
		t.Fatal("accepted rejected policy")
	}
	var count int
	r.db.QueryRow(`SELECT COUNT(*) FROM mobile_chat_photos`).Scan(&count)
	if count != 0 {
		t.Fatal("orphan photo")
	}
	// Insert a valid JPEG COM marker containing private EXIF-like data.
	data := append([]byte{}, p.Data[:2]...)
	marker := []byte("GPS-private-metadata")
	data = append(data, 0xff, 0xfe, 0, byte(len(marker)+2))
	data = append(data, marker...)
	data = append(data, p.Data[2:]...)
	photos, err := CanonicalPhotos(ctx, [][]byte{data})
	if err != nil || bytes.Contains(photos[0].Data, marker) {
		t.Fatal("metadata retained", err)
	}
	for _, raw := range [][][]byte{nil, {[]byte("broken")}, make([][]byte, 5), {make([]byte, MaxPhotoBytes+1)}} {
		if _, err = CanonicalPhotos(ctx, raw); err == nil {
			t.Fatal("invalid photo admitted")
		}
	}
}
func TestMediaKeyNeverRegeneratedForStoredPhotos(t *testing.T) {
	r, path, owner, _, _ := fixture(t)
	dir := filepath.Join(t.TempDir(), "keys")
	os.Mkdir(dir, 0o700)
	if err := InitializeMediaKey(ctx, path, dir); err != nil {
		t.Fatal(err)
	}
	key, err := LoadMediaKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.ConfigureMedia(key)
	c := create(t, r, owner)
	if _, _, err = r.Accept(ctx, owner, c.ID, Input{ClientMessageID: uuid.NewString(), Generation: 1, Photos: []Photo{testPhoto(t)}}); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, mediaKeyName))
	if err = InitializeMediaKey(ctx, path, dir); err == nil {
		t.Fatal("lost key silently reset")
	}
	if _, err = os.Stat(filepath.Join(dir, mediaKeyName)); !os.IsNotExist(err) {
		t.Fatal("replacement key exists")
	}
}
func TestExplicitFactExportOwnershipRoleAndGeneration(t *testing.T) {
	r, _, owner, other, _ := fixture(t)
	c := create(t, r, owner)
	turn := uuid.NewString()
	r.BindTurn(ctx, owner, c.ID, 1, turn, 1)
	user, _ := r.Append(ctx, owner, c.ID, 1, turn, 1, "user", "question")
	answer, _ := r.Append(ctx, owner, c.ID, 1, turn, 1, "assistant", "answer")
	for _, bad := range []struct {
		owner, msg string
		gen        uint64
	}{{other, answer.ID, 1}, {owner, user.ID, 1}, {owner, answer.ID, 2}} {
		if _, err := r.SaveAnswerFact(ctx, bad.owner, c.ID, bad.gen, bad.msg, "fact"); err == nil {
			t.Fatal("invalid source exported")
		}
	}
	id, err := r.SaveAnswerFact(ctx, owner, c.ID, 1, answer.ID, "edited fact")
	if err != nil || id < 1 {
		t.Fatal(id, err)
	}
	var who, chat, text string
	if err = r.db.QueryRow(`SELECT user_id,chat_id,content FROM facts WHERE id=?`, id).Scan(&who, &chat, &text); err != nil || who != owner || chat != "" || text != "edited fact" {
		t.Fatal("invalid account fact", err)
	}
}

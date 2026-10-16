package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/jpeg"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
)

func TestPhotosNeverSilentlyReachTextOnlyProfile(t *testing.T) {
	var calls atomic.Int32
	e, owner, c := engineFixture(t, providerFunc(func(context.Context, llm.Request) (llm.Response, error) { calls.Add(1); return llm.Response{}, nil }), nil)
	e.Repo.ConfigureMedia(make([]byte, 32))
	var b bytes.Buffer
	jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 16, 12)), nil)
	photos, err := conversations.CanonicalPhotos(context.Background(), [][]byte{b.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.Accept(context.Background(), owner, c.ID, conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: "caption", Photos: photos}); err == nil {
		t.Fatal("text-only profile admitted photo")
	}
	if calls.Load() != 0 {
		t.Fatal("provider called despite unsupported input")
	}
}
func TestPhotoPolicyRequiresExactOrderedBodies(t *testing.T) {
	a, b := []byte("first"), []byte("second")
	x, y := sha256.Sum256(a), sha256.Sum256(b)
	digests := []string{hex.EncodeToString(x[:]), hex.EncodeToString(y[:])}
	if !matchingPhotos(2, digests, [][]byte{a, b}) {
		t.Fatal("valid photos rejected")
	}
	for _, bad := range [][][]byte{nil, {a}, {b, a}, {a, []byte("changed")}} {
		if matchingPhotos(2, digests, bad) {
			t.Fatal("photo omission or change accepted")
		}
	}
	if matchingPhotos(1, digests, [][]byte{a, b}) || matchingPhotos(2, nil, nil) {
		t.Fatal("invalid policy accepted")
	}
	if !matchingPhotos(1, nil, nil) {
		t.Fatal("legacy text policy broken")
	}
}

package httpfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type attachmentTestStore struct{ *memArtifactStore }

func (s attachmentTestStore) Open(_ context.Context, owner, id string) (ArtifactReader, error) {
	return &attachmentTestReader{Reader: bytes.NewReader(s.files[id]), size: int64(len(s.files[id]))}, nil
}

type attachmentTestReader struct {
	*bytes.Reader
	size int64
}

func (r *attachmentTestReader) Close() error { return nil }
func (r *attachmentTestReader) Size() int64  { return r.size }

func TestStagedAttachmentMultipartConsentAndExactBytes(t *testing.T) {
	data := []byte{'d', 0, 255, 'e'}
	hits := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Content-Type") == "application/json" {
			var payload struct {
				Method    string `json:"method"`
				Arguments struct {
					Metainfo string `json:"metainfo"`
					Paused   bool   `json:"paused"`
				} `json:"arguments"`
			}
			if decodeErr := json.NewDecoder(r.Body).Decode(&payload); decodeErr != nil || payload.Method != "torrent-add" || payload.Arguments.Metainfo != "ZAD/ZQ==" || !payload.Arguments.Paused {
				t.Errorf("invalid RPC upload: %+v %v", payload, decodeErr)
			}
			w.WriteHeader(200)
			return
		}
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		file, _, err := r.FormFile("torrent_files[]")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer file.Close()
		got, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(got, data) || r.FormValue("paused") != "true" {
			t.Errorf("upload bytes=%v err=%v paused=%s", got, err, r.FormValue("paused"))
		}
		w.WriteHeader(200)
	}))
	defer proxy.Close()
	store := attachmentTestStore{newMemArtifactStore()}
	e := newDownloadEngine(t, proxy.URL, nil, store)
	ctx := WithScope(context.Background(), Scope{UserID: "u1", ChatID: "chat", Mode: ModeInteractive})
	view, err := NewFactory(e).StageAttachment(ctx, data, "test.torrent", "application/x-bittorrent")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ParsePlan([]byte(`{"version":1,"request":{"method":"POST","url":"http://api.example.test/transmission/upload","body":{"type":"multipart","parts":[{"name":"torrent_files[]","artifact_ref":"` + view.ID + `","filename":"test.torrent"},{"name":"paused","value":"true"}]},"response":{"mode":"inline"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Run(ctx, plan)
	if err != nil || res.Outcome != OutcomeConfirmationRequired || hits != 0 {
		t.Fatalf("unconfirmed upload: %+v %v hits=%d", res, err, hits)
	}
	e.confirmer = &approvingConfirmer{}
	res, err = e.Run(ctx, plan)
	if err != nil || res.Outcome != OutcomeCompleted || hits != 1 {
		t.Fatalf("confirmed upload: %+v %v hits=%d", res, err, hits)
	}
	jsonPlan, parseErr := ParsePlan([]byte(`{"version":1,"request":{"method":"POST","url":"http://api.example.test/transmission/rpc","body":{"type":"json","value":{"method":"torrent-add","arguments":{"metainfo":{"$artifact_base64":"` + view.ID + `"},"paused":true}}},"response":{"mode":"inline"}}}`))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	res, err = e.Run(ctx, jsonPlan)
	if err != nil || res.Outcome != OutcomeCompleted || hits != 2 {
		t.Fatalf("RPC upload failed: %+v %v hits=%d", res, err, hits)
	}
	e.limits.MaxUploadBytes = 2
	if _, err := NewFactory(e).StageAttachment(ctx, data, "test.torrent", ""); err == nil {
		t.Fatal("upload limit bypass")
	}
}

func TestArtifactBase64JSONPrivateResolution(t *testing.T) {
	data := []byte{0, 255, 'd', 'e'}
	store := attachmentTestStore{newMemArtifactStore()}
	e := newDownloadEngine(t, "http://127.0.0.1:12345", nil, store)
	ctx := WithScope(context.Background(), Scope{UserID: "u1", ChatID: "chat"})
	view, err := NewFactory(e).StageAttachment(ctx, data, "test.torrent", "application/x-bittorrent")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"method":"torrent-add","arguments":{"metainfo":{"$artifact_base64":"` + view.ID + `"},"paused":true}}`)
	if _, walkErr := walkJSONRefs(raw); walkErr != nil {
		t.Fatal(walkErr)
	}
	got, err := e.resolveJSONRefs(ctx, raw, &runState{}, Scope{UserID: "u1", ChatID: "chat"}, Origin{})
	if err != nil {
		t.Fatal(err)
	}
	args := got.(map[string]any)["arguments"].(map[string]any)
	if args["metainfo"] != "AP9kZQ==" || args["paused"] != true {
		t.Fatalf("resolved JSON: %+v", got)
	}
	for _, bad := range []string{`{"$artifact_base64":""}`, `{"$artifact_base64":1}`, `{"$artifact_base64":"id","extra":true}`} {
		if _, walkErr := walkJSONRefs([]byte(bad)); walkErr == nil {
			t.Fatalf("accepted invalid reference %s", bad)
		}
	}
	lit, err := e.resolveJSONRefs(ctx, []byte(`{"$literal":{"$artifact_base64":"not-read"}}`), &runState{}, Scope{}, Origin{})
	if err != nil || lit.(map[string]any)["$artifact_base64"] != "not-read" {
		t.Fatalf("literal evaluated: %v %v", lit, err)
	}
	if _, err := e.resolveJSONRefs(ctx, raw, &runState{}, Scope{}, Origin{}); err == nil {
		t.Fatal("unscoped JSON attachment read")
	}
}

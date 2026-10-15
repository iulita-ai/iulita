package transport

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	"google.golang.org/protobuf/proto"
)

func TestArtifactChunkBudgetRejectsIrrecoverableSlots(t *testing.T) {
	scenario := scenarios()[0]
	scenario.Reasoning = true
	h := setupHarnessWithOptions(t, devices.Options{UI: true, Vision: true, Scenarios: []devices.Scenario{scenario}})
	token, device := h.login(t, "owner")
	native, task, _ := createUI(t, h, token, device, scenario, true)
	uiSessions.Store(task.TaskId, native.session)
	defer uiSessions.Delete(task.TaskId)
	tree := uiTree(scenario)
	pushTree(t, h, token, task, tree)
	upload := &v1.UploadArtifactRequest{Client: clientContext(), Operation: operation(), UploadId: uuid.NewString(), Binding: &v1.ArtifactBinding{TaskId: task.TaskId, SessionId: native.session, SessionGeneration: 1, SnapshotId: tree.SnapshotId, WindowId: tree.WindowId, PackageName: tree.PackageName, ConsentRevision: devices.PolicyRevision}, MediaType: v1.ArtifactMediaType_ARTIFACT_MEDIA_TYPE_PNG, TotalBytes: 128, ContentSha256: sha256Sum(bytes.Repeat([]byte{1}, 128)), Chunk: []byte{1}}
	client := v1.NewArtifactServiceClient(h.conn)
	if _, err := client.Upload(tokenContext(token), upload); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.Open(context.Background(), h.paths[1], false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Seed only the historical chunk metadata to exercise the admission boundary
	// without 64 timing-dependent network writes. No image bytes are persisted.
	for offset := 1; offset < 63; offset++ {
		if _, err = db.Exec(`INSERT INTO mobile_artifact_chunks VALUES(?,?,?,1)`, upload.UploadId, offset, sha256Sum([]byte{1})); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`UPDATE mobile_artifacts SET received=63 WHERE upload_id=?`, upload.UploadId); err != nil {
		t.Fatal(err)
	}
	last := proto.Clone(upload).(*v1.UploadArtifactRequest)
	last.Operation = operation()
	last.Offset = 63
	if _, err = client.Upload(tokenContext(token), last); err == nil {
		t.Fatal("last available slot accepted an incomplete object")
	}
	// A legacy partial upload already containing 64 chunks is refused before any
	// additional RAM object, chunk metadata or successful ACK can be created.
	if _, err = db.Exec(`INSERT INTO mobile_artifact_chunks VALUES(?,63,?,1)`, upload.UploadId, sha256Sum([]byte{1})); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE mobile_artifacts SET received=64 WHERE upload_id=?`, upload.UploadId); err != nil {
		t.Fatal(err)
	}
	last.Operation = operation()
	last.Offset = 64
	if _, err = client.Upload(tokenContext(token), last); err == nil {
		t.Fatal("65th new chunk acknowledged")
	}
	var count, received int
	if err = db.QueryRow(`SELECT COUNT(*) FROM mobile_artifact_chunks WHERE upload_id=?`, upload.UploadId).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT received FROM mobile_artifacts WHERE upload_id=?`, upload.UploadId).Scan(&received); err != nil {
		t.Fatal(err)
	}
	if count != 64 || received != 64 {
		t.Fatal("rejected chunk changed admission state", count, received)
	}
}

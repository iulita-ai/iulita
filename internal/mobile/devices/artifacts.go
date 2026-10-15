package devices

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strconv"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	_ "golang.org/x/image/webp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *Store) artifactAuthority(ctx context.Context, tx *sql.Tx, c *oauthstore.Claims, id string, source bool) (Task, error) {
	if !s.options.Vision {
		return Task{}, ErrUnavailable
	}
	t, err := taskIn(ctx, tx, c.Subject, id)
	if err != nil {
		return t, ErrUnavailable
	}
	if source && t.Device != c.DeviceID || !source && t.ViewerDevice != c.DeviceID || !isUI(t.Action) || t.Cancelled || !nativeSessionLive(ctx, tx, t) || !s.uiLeaseLive(ctx, tx, t) || !s.originFamilyLive(ctx, tx, t) || s.chats.OriginAllowed(ctx, t.Owner, t.Conversation, t.Turn, t.Generation) != nil || s.externalLive(ctx, tx, t) != nil {
		return t, ErrUnavailable
	}
	var body []byte
	if tx.QueryRowContext(ctx, `SELECT metadata FROM mobile_native_sessions WHERE owner_id=? AND device_id=? AND id=? AND generation=?`, t.Owner, t.Device, t.Session, t.SessionGeneration).Scan(&body) != nil {
		return t, ErrUnavailable
	}
	mirror := new(v1.SessionMirror)
	if proto.Unmarshal(body, mirror) != nil || !mirror.ScreenshotAllowed || !mirror.ScreenContentAllowed {
		return t, ErrUnavailable
	}
	return t, nil
}

// UploadArtifact admits bounded, idempotent chunks of an approved window capture.
func (s *Store) UploadArtifact(ctx context.Context, identity Identity, in *v1.UploadArtifactRequest) (*v1.Artifact, error) {
	if in == nil || in.Operation == nil || !validID(in.Operation.OperationId) || !validID(in.UploadId) || in.Binding == nil || in.TotalBytes == 0 || in.TotalBytes > 1<<20 || len(in.Chunk) == 0 || len(in.Chunk) > 64<<10 || in.Offset > in.TotalBytes || uint64(len(in.Chunk)) > in.TotalBytes-in.Offset || len(in.ContentSha256) != 32 || in.MediaType < v1.ArtifactMediaType_ARTIFACT_MEDIA_TYPE_PNG || in.MediaType > v1.ArtifactMediaType_ARTIFACT_MEDIA_TYPE_WEBP {
		return nil, ErrInvalid
	}
	clone := new(v1.UploadArtifactRequest)
	proto.Merge(clone, in)
	clone.Client = nil
	clone.Operation = nil
	digest, err := semantic(clone)
	if err != nil {
		return nil, err
	}
	var result *v1.Artifact
	err = s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if stepErr1 := scope(c, "devices:sync"); stepErr1 != nil {
			return stepErr1
		}
		t, stepErr2 := s.artifactAuthority(ctx, tx, c, in.Binding.TaskId, true)
		if stepErr2 != nil {
			return stepErr2
		}
		var prior []byte
		var device, ack string
		lookup := tx.QueryRowContext(ctx, `SELECT digest,device_id,body_ref FROM mobile_phone_operations WHERE owner_id=? AND client_id=? AND operation_id=? AND kind='upload'`, c.Subject, c.ClientID, in.Operation.OperationId).Scan(&prior, &device, &ack)
		if lookup == nil {
			if !bytes.Equal(prior, digest) || device != c.DeviceID {
				return ErrInvalid
			}
			result = new(v1.Artifact)
			if json.Unmarshal([]byte(ack), result) != nil {
				return ErrUnavailable
			}
			return nil
		}
		if !errors.Is(lookup, sql.ErrNoRows) {
			return lookup
		}
		if stepErr3 := operationBudget(ctx, tx, c.Subject); stepErr3 != nil {
			return stepErr3
		}
		bindingDigest, stepErr2 := semantic(in.Binding)
		if stepErr2 != nil {
			return stepErr2
		}
		var total, received uint64
		var complete bool
		var expiry int64
		var oldBinding, oldContent []byte
		var media v1.ArtifactMediaType
		lookup = tx.QueryRowContext(ctx, `SELECT total_bytes,received,complete,expires_at,binding_digest,content_digest,media_type FROM mobile_artifacts WHERE upload_id=? AND owner_id=? AND device_id=? AND task_id=?`, in.UploadId, c.Subject, c.DeviceID, t.ID).Scan(&total, &received, &complete, &expiry, &oldBinding, &oldContent, &media)
		switch {
		case errors.Is(lookup, sql.ErrNoRows):
			tree, stepErr4 := s.planningSnapshot(ctx, tx, t)
			if stepErr4 != nil {
				return stepErr4
			}
			if !tree.SingleWindowCapture || in.Binding.SnapshotId != tree.SnapshotId || in.Binding.WindowId != tree.WindowId || in.Binding.PackageName != tree.PackageName || in.Binding.SessionId != t.Session || in.Binding.SessionGeneration != t.SessionGeneration || in.Binding.ConsentRevision != t.Consent {
				return ErrUnavailable
			}
			var count int
			if stepErr9 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_artifacts WHERE task_id=?`, t.ID).Scan(&count); stepErr9 != nil {
				return stepErr9
			}
			if count >= 4 || in.Offset != 0 {
				return ErrBusy
			}
			expiry = expiryBound(time.Now().Add(15*time.Minute), t.ExpiresAt).UnixMilli()
			total = in.TotalBytes
			media = in.MediaType
			captureBinding, captureErr := proto.Marshal(in.Binding)
			if captureErr != nil {
				return captureErr
			}
			if _, stepErr10 := tx.ExecContext(ctx, `INSERT INTO mobile_artifacts(upload_id,task_id,owner_id,device_id,viewer_device_id,binding_digest,binding,content_digest,media_type,total_bytes,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, in.UploadId, t.ID, c.Subject, c.DeviceID, t.ViewerDevice, bindingDigest, captureBinding, in.ContentSha256, media, total, expiry); stepErr10 != nil {
				return stepErr10
			}
		case lookup != nil:
			return lookup
		case !bytes.Equal(bindingDigest, oldBinding) || !bytes.Equal(in.ContentSha256, oldContent) || total != in.TotalBytes || media != in.MediaType:
			return ErrInvalid
		}
		if expiry <= time.Now().UnixMilli() {
			return ErrUnavailable
		}
		chunkDigest := sha256Bytes(in.Chunk)
		if in.Offset < received {
			var old []byte
			var size int
			if tx.QueryRowContext(ctx, `SELECT digest,size FROM mobile_artifact_chunks WHERE upload_id=? AND offset=?`, in.UploadId, in.Offset).Scan(&old, &size) != nil || !bytes.Equal(old, chunkDigest) || size != len(in.Chunk) {
				return ErrInvalid
			}
		} else {
			if in.Offset != received || complete {
				return ErrInvalid
			}
			var chunkCount int
			if countErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_artifact_chunks WHERE upload_id=?`, in.UploadId).Scan(&chunkCount); countErr != nil {
				return countErr
			}
			// Acknowledging a 65th chunk (or a short final available slot) would
			// create an upload the bounded assembler can never complete.
			if chunkCount >= 64 || chunkCount == 63 && received+uint64(len(in.Chunk)) < total {
				return ErrInvalid
			}
			ref := in.UploadId + ":" + strconv.FormatUint(in.Offset, 10)
			binding := s.binding(t.Owner, t.Device, t.ID, t.Destination, t.Consent, "artifact")
			if stepErr5 := s.content.Put(ref, binding, in.Chunk, time.UnixMilli(expiry)); stepErr5 != nil {
				return stepErr5
			}
			if _, stepErr6 := tx.ExecContext(ctx, `INSERT INTO mobile_artifact_chunks VALUES(?,?,?,?)`, in.UploadId, in.Offset, chunkDigest, len(in.Chunk)); stepErr6 != nil {
				return stepErr6
			}
			received += uint64(len(in.Chunk))
		}
		if in.Finalize && !complete {
			if received != total {
				return ErrInvalid
			}
			body, stepErr7 := s.assemble(ctx, tx, t, in.UploadId, total)
			if stepErr7 != nil {
				return stepErr7
			}
			defer clear(body)
			config, format, stepErr7 := image.DecodeConfig(bytes.NewReader(body))
			formats := map[v1.ArtifactMediaType]string{1: "png", 2: "jpeg", 3: "webp"}
			if stepErr7 != nil || format != formats[media] || config.Width <= 0 || config.Height <= 0 || config.Width > 4096 || config.Height > 4096 || !bytes.Equal(sha256Bytes(body), in.ContentSha256) {
				return ErrInvalid
			}
			if stepErr11 := s.content.Put(in.UploadId, s.binding(t.Owner, t.Device, t.ID, t.Destination, t.Consent, "artifact"), body, time.UnixMilli(expiry)); stepErr11 != nil {
				return stepErr11
			}
			complete = true
		}
		if _, stepErr8 := tx.ExecContext(ctx, `UPDATE mobile_artifacts SET received=?,complete=? WHERE upload_id=?`, received, complete, in.UploadId); stepErr8 != nil {
			return stepErr8
		}
		result = &v1.Artifact{ArtifactId: in.UploadId, UploadId: in.UploadId, CommittedBytes: received, Complete: complete, ExpiresAt: timestamppb.New(time.UnixMilli(expiry))}
		encoded, stepErr2 := metadata(result)
		if stepErr2 != nil {
			return stepErr2
		}
		_, stepErr2 = tx.ExecContext(ctx, `INSERT INTO mobile_phone_operations VALUES(?,?,?,'upload',?,?,'accepted',?,?,?)`, c.Subject, c.ClientID, in.Operation.OperationId, c.DeviceID, digest, string(encoded), expiry, time.Now().UnixMilli())
		return stepErr2
	})
	return result, err
}
func (s *Store) assemble(ctx context.Context, tx *sql.Tx, t Task, id string, total uint64) ([]byte, error) {
	rows, err := tx.QueryContext(ctx, `SELECT offset,size FROM mobile_artifact_chunks WHERE upload_id=? ORDER BY offset LIMIT 64`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // Read-only cursor cleanup.
	body := make([]byte, 0, total)
	for rows.Next() {
		var offset uint64
		var size int
		if err := rows.Scan(&offset, &size); err != nil {
			return nil, err
		}
		if offset != uint64(len(body)) {
			return nil, ErrInvalid
		}
		chunk, _, err := s.content.Get(id+":"+strconv.FormatUint(offset, 10), s.binding(t.Owner, t.Device, t.ID, t.Destination, t.Consent, "artifact"))
		if err != nil {
			return nil, err
		}
		if len(chunk) != size || len(body)+size > 1<<20 {
			clear(chunk)
			return nil, ErrInvalid
		}
		body = append(body, chunk...)
		clear(chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if uint64(len(body)) != total {
		clear(body)
		return nil, ErrInvalid
	}
	return body, nil
}

// ReadArtifact returns bounded bytes only to the current verified viewer.
func (s *Store) ReadArtifact(ctx context.Context, identity Identity, in *v1.ReadArtifactRequest) (*v1.ArtifactChunk, error) {
	if in == nil || !validID(in.ArtifactId) || in.MaxBytes > 64<<10 {
		return nil, ErrInvalid
	}
	var result *v1.ArtifactChunk
	err := s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if err := scope(c, "devices:read"); err != nil {
			return err
		}
		var taskID string
		var expiry int64
		if tx.QueryRowContext(ctx, `SELECT task_id,expires_at FROM mobile_artifacts WHERE upload_id=? AND owner_id=? AND viewer_device_id=? AND complete=1`, in.ArtifactId, c.Subject, c.DeviceID).Scan(&taskID, &expiry) != nil || expiry <= time.Now().UnixMilli() {
			return ErrUnavailable
		}
		t, err := s.artifactAuthority(ctx, tx, c, taskID, false)
		if err != nil {
			return err
		}
		body, expires, err := s.content.Get(in.ArtifactId, s.binding(t.Owner, t.Device, t.ID, t.Destination, t.Consent, "artifact"))
		if err != nil {
			return err
		}
		defer clear(body)
		if in.Offset > uint64(len(body)) {
			return ErrInvalid
		}
		n := in.MaxBytes
		if n == 0 {
			n = 64 << 10
		}
		end := min(in.Offset+uint64(n), uint64(len(body)))
		result = &v1.ArtifactChunk{ArtifactId: in.ArtifactId, Offset: in.Offset, Chunk: append([]byte(nil), body[in.Offset:end]...), Eof: end == uint64(len(body)), ExpiresAt: timestamppb.New(expires)}
		return nil
	})
	return result, err
}

func (s *Store) processingImage(ctx context.Context, tx *sql.Tx, c *oauthstore.Claims, t Task, id string) (imageBytes []byte, mediaType string, err error) {
	if _, stepErr12 := s.artifactAuthority(ctx, tx, c, t.ID, false); stepErr12 != nil {
		return nil, "", stepErr12
	}
	var media v1.ArtifactMediaType
	if tx.QueryRowContext(ctx, `SELECT media_type FROM mobile_artifacts WHERE upload_id=? AND task_id=? AND owner_id=? AND viewer_device_id=? AND complete=1 AND expires_at>?`, id, t.ID, c.Subject, c.DeviceID, time.Now().UnixMilli()).Scan(&media) != nil {
		return nil, "", ErrUnavailable
	}
	body, _, err := s.content.Get(id, s.binding(t.Owner, t.Device, t.ID, t.Destination, t.Consent, "artifact"))
	return body, map[v1.ArtifactMediaType]string{1: "image/png", 2: "image/jpeg", 3: "image/webp"}[media], err
}

package devices

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"math"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/ephemeral"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"google.golang.org/protobuf/proto"
)

// TelemetryRevision binds passive history storage to the telemetry consent schema.
const TelemetryRevision = "mobile-telemetry-v1"

func telemetryCategory(r *v1.TelemetryRecord) string {
	if r.GetStatus() != nil {
		return "status"
	}
	if r.GetLocation() != nil {
		return "location"
	}
	if r.GetNotification() != nil {
		return "notifications"
	}
	return ""
}
func validateTelemetry(r *v1.TelemetryRecord, now time.Time) error {
	if r == nil || !validID(r.RecordId) || !validID(r.CollectionId) || r.CapturedAt == nil || r.CapturedAt.CheckValid() != nil || len(r.ProtoReflect().GetUnknown()) > 0 || len(r.CapturedAt.ProtoReflect().GetUnknown()) > 0 || proto.Size(r) > 16384 || telemetryCategory(r) == "" {
		return ErrInvalid
	}
	if !r.CapturedAt.AsTime().Add(24*time.Hour).After(now) || r.CapturedAt.AsTime().After(now.Add(time.Minute)) {
		return ephemeral.ErrExpired
	}
	if x := r.GetStatus(); x != nil {
		if len(x.ProtoReflect().GetUnknown()) > 0 || x.GetBatteryPercent() > 100 || len(x.Model) > 128 || len(x.OsVersion) > 128 || len(x.NetworkType) > 32 {
			return ErrInvalid
		}
	}
	if x := r.GetLocation(); x != nil {
		if len(x.ProtoReflect().GetUnknown()) > 0 || math.IsNaN(x.Latitude) || math.IsNaN(x.Longitude) || math.IsNaN(x.AccuracyMeters) || math.IsInf(x.Latitude, 0) || math.IsInf(x.Longitude, 0) || math.IsInf(x.AccuracyMeters, 0) || x.Latitude < -90 || x.Latitude > 90 || x.Longitude < -180 || x.Longitude > 180 || x.AccuracyMeters < 0 || x.AccuracyMeters > 100000 || len(x.Provider) > 64 {
			return ErrInvalid
		}
	}
	if x := r.GetNotification(); x != nil {
		if len(x.ProtoReflect().GetUnknown()) > 0 || !packageName.MatchString(x.PackageName) || len(x.PackageName) > 200 || len(x.Title) > 512 || len(x.Text) > 8192 || x.PostedAt == nil || x.PostedAt.CheckValid() != nil || len(x.PostedAt.ProtoReflect().GetUnknown()) > 0 || x.PostedAt.AsTime().After(r.CapturedAt.AsTime().Add(time.Minute)) || x.PostedAt.AsTime().Before(r.CapturedAt.AsTime().Add(-24*time.Hour)) {
			return ErrInvalid
		}
	}
	return nil
}
func telemetryBinding(owner, device, id, collection, category string) ephemeral.Binding {
	return ephemeral.Binding{Owner: owner, Device: device, Task: id, Destination: "mobile.telemetry:" + collection, ConsentRevision: TelemetryRevision, Category: category}
}
func telemetryKey(owner, device, id string) string {
	return "telemetry:" + owner + ":" + device + ":" + id
}

// UploadTelemetry accepts immutable records independently of live task leases.
// Replaying identical records is safe, including after an acknowledgement loss
// or loss of the server's encrypted RAM. Capture age is never reset by upload.
func (s *Store) UploadTelemetry(ctx context.Context, identity Identity, records []*v1.TelemetryRecord) ([]string, error) {
	if len(records) == 0 || len(records) > 100 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, r := range records {
		if err := validateTelemetry(r, s.clock()); err != nil {
			return nil, err
		}
		if seen[r.RecordId] {
			return nil, ErrInvalid
		}
		seen[r.RecordId] = true
	}
	var ids []string
	created := map[string]ephemeral.Binding{}
	err := s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if !oauthstore.HasScope(c, "devices:sync") {
			return ErrUnavailable
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_telemetry_records WHERE owner_id=? AND device_id=? AND received_at>?`, c.Subject, c.DeviceID, s.clock().Add(-24*time.Hour).UnixMilli()).Scan(&count); err != nil {
			return err
		}
		for _, r := range records {
			if err := validateTelemetry(r, s.clock()); err != nil {
				return err
			}
			raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(r)
			if err != nil {
				return ErrInvalid
			}
			digest := sha256.Sum256(raw)
			var old []byte
			expiry := r.CapturedAt.AsTime().Add(24 * time.Hour)
			if max := s.clock().Add(24 * time.Hour); expiry.After(max) {
				expiry = max
			}
			expiry = time.UnixMilli(expiry.UnixMilli())
			var oldExpiry int64
			err = tx.QueryRowContext(ctx, `SELECT digest,expires_at FROM mobile_telemetry_records WHERE owner_id=? AND device_id=? AND id=?`, c.Subject, c.DeviceID, r.RecordId).Scan(&old, &oldExpiry)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				clear(raw)
				return err
			}
			if err == nil {
				expiry = time.UnixMilli(oldExpiry)
			}
			if err == nil && !bytes.Equal(old, digest[:]) {
				clear(raw)
				return conversations.ErrIdempotency
			}
			if errors.Is(err, sql.ErrNoRows) {
				count++
				if count > 10000 {
					clear(raw)
					return ErrBusy
				}
			}
			category := telemetryCategory(r)
			binding := telemetryBinding(c.Subject, c.DeviceID, r.RecordId, r.CollectionId, category)
			key := telemetryKey(c.Subject, c.DeviceID, r.RecordId)
			prior, _, missing := s.history.Get(key, binding)
			clear(prior)
			err = s.history.Put(key, binding, raw, expiry)
			length := len(raw)
			clear(raw)
			if err != nil {
				return err
			}
			if missing != nil {
				created[key] = binding
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO mobile_telemetry_records VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(owner_id,device_id,id) DO UPDATE SET family_id=excluded.family_id`, c.Subject, c.DeviceID, r.RecordId, r.CollectionId, c.FamilyID, category, digest[:], r.CapturedAt.AsTime().UnixMilli(), expiry.UnixMilli(), s.clock().UnixMilli(), length)
			if err != nil {
				return err
			}
			ids = append(ids, r.RecordId)
		}
		return nil
	})
	if err != nil {
		for key, binding := range created {
			s.history.Delete(key, binding)
		}
		return nil, err
	}
	return ids, nil
}

// ListTelemetry reads retained history only for the token device and current family.
func (s *Store) ListTelemetry(ctx context.Context, identity Identity, since time.Time, before string, limit int) ([]*v1.TelemetryRecord, string, error) {
	if limit < 1 || limit > 100 || since.After(s.clock()) || since.Before(s.clock().Add(-24*time.Hour-time.Minute)) || (before != "" && !validID(before)) {
		return nil, "", ErrInvalid
	}
	var result []*v1.TelemetryRecord
	next := ""
	err := s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if !oauthstore.HasScope(c, "devices:read") {
			return ErrUnavailable
		}
		beforeTime := s.clock().Add(time.Minute).UnixMilli()
		beforeID := "ffffffff-ffff-ffff-ffff-ffffffffffff"
		if before != "" {
			if err := tx.QueryRowContext(ctx, `SELECT captured_at,id FROM mobile_telemetry_records WHERE owner_id=? AND device_id=? AND family_id=? AND id=?`, c.Subject, c.DeviceID, c.FamilyID, before).Scan(&beforeTime, &beforeID); err != nil {
				return ErrUnavailable
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,collection_id,category FROM mobile_telemetry_records WHERE owner_id=? AND device_id=? AND family_id=? AND expires_at>? AND captured_at>=? AND (captured_at<? OR (captured_at=? AND id<?)) ORDER BY captured_at DESC,id DESC LIMIT ?`, c.Subject, c.DeviceID, c.FamilyID, s.clock().UnixMilli(), since.UnixMilli(), beforeTime, beforeTime, beforeID, 10001)
		if err != nil {
			return err
		}
		defer rows.Close() //nolint:errcheck // Read cleanup only; preserve rows.Err below.
		for rows.Next() {
			var id, collection, category string
			if err := rows.Scan(&id, &collection, &category); err != nil {
				return err
			}
			raw, _, err := s.history.Get(telemetryKey(c.Subject, c.DeviceID, id), telemetryBinding(c.Subject, c.DeviceID, id, collection, category))
			if err != nil {
				continue
			}
			if len(result) == limit {
				clear(raw)
				next = result[len(result)-1].RecordId
				break
			}
			record := new(v1.TelemetryRecord)
			err = proto.Unmarshal(raw, record)
			clear(raw)
			if err != nil {
				return ErrInvalid
			}
			result = append(result, record)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	return result, next, nil
}

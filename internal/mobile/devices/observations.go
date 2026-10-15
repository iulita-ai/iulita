package devices

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/ephemeral"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func observationCategory(o *v1.Observation) string {
	switch o.Category {
	case v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS:
		if o.GetStatus() != nil {
			return "status"
		}
	case v1.ObservationCategory_OBSERVATION_CATEGORY_LOCATION:
		if o.GetLocation() != nil {
			return "location"
		}
	case v1.ObservationCategory_OBSERVATION_CATEGORY_NOTIFICATIONS:
		if o.GetNotifications() != nil {
			return "notifications"
		}
	}
	return ""
}
func validateObservation(o *v1.Observation) error {
	if o == nil || !validID(o.ObservationId) || !validID(o.TaskId) || !validID(o.SessionId) || o.SessionGeneration == 0 || o.CollectedAt == nil || o.ExpiresAt == nil || o.CollectedAt.CheckValid() != nil || o.ExpiresAt.CheckValid() != nil {
		return ErrInvalid
	}
	if !o.ExpiresAt.AsTime().After(time.Now()) || o.ExpiresAt.AsTime().After(o.CollectedAt.AsTime().Add(15*time.Minute)) || o.CollectedAt.AsTime().After(time.Now().Add(30*time.Second)) {
		return ephemeral.ErrExpired
	}
	if observationCategory(o) == "" {
		return ErrInvalid
	}
	if s := o.GetStatus(); s != nil {
		if s.GetBatteryPercent() > 100 || len(s.Model) > 128 || len(s.OsVersion) > 128 || len(s.NetworkType) > 32 {
			return ErrInvalid
		}
	}
	if l := o.GetLocation(); l != nil {
		if math.IsNaN(l.Latitude) || math.IsNaN(l.Longitude) || math.IsNaN(l.AccuracyMeters) || math.IsInf(l.Latitude, 0) || math.IsInf(l.Longitude, 0) || math.IsInf(l.AccuracyMeters, 0) || l.Latitude < -90 || l.Latitude > 90 || l.Longitude < -180 || l.Longitude > 180 || l.AccuracyMeters < 0 || l.AccuracyMeters > 100000 || len(l.Provider) > 64 {
			return ErrInvalid
		}
	}
	if batch := o.GetNotifications(); batch != nil {
		if len(batch.Notifications) > 50 {
			return ErrInvalid
		}
		for _, n := range batch.Notifications {
			if n == nil || !packageName.MatchString(n.PackageName) || len(n.PackageName) > 200 || len(n.Title) > 512 || len(n.Text) > 8192 || n.PostedAt == nil || n.PostedAt.CheckValid() != nil || n.PostedAt.AsTime().After(o.CollectedAt.AsTime().Add(30*time.Second)) {
				return ErrInvalid
			}
		}
	}
	if proto.Size(o) > 1<<20 {
		return ErrInvalid
	}
	return nil
}

// Sync atomically admits an idempotent observation batch under live authority.
func (s *Store) Sync(ctx context.Context, identity Identity, operation string, observations []*v1.Observation) ([]string, error) {
	if !validID(operation) || len(observations) == 0 || len(observations) > 16 {
		return nil, ErrInvalid
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&v1.SyncObservationsRequest{Observations: observations})
	if err != nil {
		return nil, ErrInvalid
	}
	operationDigest := sha256.Sum256(payload)
	clear(payload)
	var ids []string
	created := map[string]ephemeral.Binding{}
	err = s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if !oauthstore.HasScope(c, "devices:sync") {
			return ErrUnavailable
		}
		var priorDigest, accepted []byte
		var priorDevice string
		stepErr1 := tx.QueryRowContext(ctx, `SELECT digest,accepted_ids,device_id FROM mobile_sync_operations WHERE owner_id=? AND client_id=? AND operation_id=?`, c.Subject, c.ClientID, operation).Scan(&priorDigest, &accepted, &priorDevice)
		if stepErr1 == nil {
			if priorDevice != c.DeviceID || !bytes.Equal(priorDigest, operationDigest[:]) {
				return conversations.ErrIdempotency
			}
			if json.Unmarshal(accepted, &ids) != nil {
				return ErrInvalid
			}
			return nil
		}
		if !errors.Is(stepErr1, sql.ErrNoRows) {
			return stepErr1
		}
		var count int
		if checkErr7 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_sync_operations WHERE owner_id=? AND created_at>?`, c.Subject, time.Now().Add(-7*24*time.Hour).UnixMilli()).Scan(&count); checkErr7 != nil {
			return checkErr7
		}
		if count >= 10000 {
			return ErrBusy
		}
		tasks := map[string]bool{}
		for _, o := range observations {
			if stepErr2 := validateObservation(o); stepErr2 != nil {
				return stepErr2
			}
			tasks[o.TaskId] = true
		}
		for task := range tasks {
			if checkErr8 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_sync_task_operations WHERE owner_id=? AND task_id=?`, c.Subject, task).Scan(&count); checkErr8 != nil {
				return checkErr8
			}
			if count >= 64 {
				return ErrBusy
			}
		}
		for _, o := range observations {
			task, stepErr3 := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM mobile_phone_tasks WHERE id=? AND owner_id=? AND device_id=?`, o.TaskId, c.Subject, c.DeviceID))
			if stepErr3 != nil {
				return stepErr3
			}
			if task.Cancelled || !nativeSessionLive(ctx, tx, task) || !s.originFamilyLive(ctx, tx, task) || task.Session != o.SessionId || task.SessionGeneration != o.SessionGeneration || o.CollectedAt.AsTime().After(task.ExpiresAt) || task.CreatedAt.After(o.CollectedAt.AsTime().Add(30*time.Second)) || (task.State != "running" && task.State != "succeeded") {
				return ErrUnavailable
			}
			if checkErr9 := s.chats.OriginAllowed(ctx, task.Owner, task.Conversation, task.Turn, task.Generation); checkErr9 != nil {
				return checkErr9
			}
			if (task.Action == v1.ActionKind_ACTION_KIND_READ_STATUS && o.Category != v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS) || (task.Action == v1.ActionKind_ACTION_KIND_READ_LOCATION && o.Category != v1.ObservationCategory_OBSERVATION_CATEGORY_LOCATION) || (task.Action == v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS && o.Category != v1.ObservationCategory_OBSERVATION_CATEGORY_NOTIFICATIONS) || task.Action > v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS {
				return ErrInvalid
			}
			var sessionBytes []byte
			if tx.QueryRowContext(ctx, `SELECT metadata FROM mobile_native_sessions WHERE owner_id=? AND device_id=? AND id=? AND generation=?`, task.Owner, task.Device, task.Session, task.SessionGeneration).Scan(&sessionBytes) != nil {
				return ErrUnavailable
			}
			var session v1.SessionMirror
			if proto.Unmarshal(sessionBytes, &session) != nil {
				return ErrInvalid
			}
			if batch := o.GetNotifications(); batch != nil {
				for _, n := range batch.Notifications {
					allowed := false
					for _, p := range session.AllowedPackages {
						if n.PackageName == p {
							allowed = true
						}
					}
					if !allowed {
						return ErrUnavailable
					}
				}
			}
			body, stepErr3 := proto.MarshalOptions{Deterministic: true}.Marshal(o)
			if stepErr3 != nil {
				return ErrInvalid
			}
			digest := sha256.Sum256(body)
			var old []byte
			var expiry int64
			stepErr3 = tx.QueryRowContext(ctx, `SELECT digest,expires_at FROM mobile_phone_observations WHERE id=? AND owner_id=? AND device_id=? AND task_id=?`, o.ObservationId, task.Owner, task.Device, task.ID).Scan(&old, &expiry)
			if stepErr3 == nil {
				clear(body)
				if !bytes.Equal(old, digest[:]) {
					return ErrInvalid
				}
				if expiry <= time.Now().UnixMilli() {
					return ephemeral.ErrExpired
				}
				if _, _, stepErr3 = s.content.Get(o.ObservationId, s.binding(task.Owner, task.Device, task.ID, task.Destination, task.Consent, observationCategory(o))); stepErr3 != nil {
					return stepErr3
				}
				ids = append(ids, o.ObservationId)
				continue
			}
			if !errors.Is(stepErr3, sql.ErrNoRows) {
				clear(body)
				return stepErr3
			}
			var count, total int
			if stepErr3 = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(bytes),0) FROM mobile_phone_observations WHERE task_id=?`, task.ID).Scan(&count, &total); stepErr3 != nil {
				clear(body)
				return stepErr3
			}
			if count >= 16 || total+len(body) > 1<<20 {
				clear(body)
				return ErrBusy
			}
			if stepErr3 = s.content.Put(o.ObservationId, s.binding(task.Owner, task.Device, task.ID, task.Destination, task.Consent, observationCategory(o)), body, o.ExpiresAt.AsTime()); stepErr3 != nil {
				clear(body)
				return stepErr3
			}
			created[o.ObservationId] = s.binding(task.Owner, task.Device, task.ID, task.Destination, task.Consent, observationCategory(o))
			_, stepErr3 = tx.ExecContext(ctx, `INSERT INTO mobile_phone_observations VALUES(?,?,?,?,?,?,?,?,?)`, o.ObservationId, task.Owner, task.Device, task.ID, o.Category, digest[:], o.ExpiresAt.AsTime().UnixMilli(), time.Now().UnixMilli(), len(body))
			clear(body)
			if stepErr3 != nil {
				return stepErr3
			}
			ids = append(ids, o.ObservationId)
		}
		accepted, stepErr1 = json.Marshal(ids)
		if stepErr1 != nil {
			return stepErr1
		}
		_, stepErr1 = tx.ExecContext(ctx, `INSERT INTO mobile_sync_operations VALUES(?,?,?,?,?,?,?)`, c.Subject, c.ClientID, operation, c.DeviceID, operationDigest[:], accepted, time.Now().UnixMilli())
		if stepErr1 != nil {
			return stepErr1
		}
		for task := range tasks {
			if _, stepErr1 = tx.ExecContext(ctx, `INSERT INTO mobile_sync_task_operations VALUES(?,?,?)`, c.Subject, task, operation); stepErr1 != nil {
				return stepErr1
			}
		}
		return stepErr1
	})
	if err != nil {
		for id, binding := range created {
			s.content.Delete(id, binding)
		}
		return nil, err
	}
	return ids, err
}

// Result returns ephemeral task content only while all authority remains valid.
func (s *Store) Result(ctx context.Context, identity Identity, taskID string) (*v1.EphemeralResult, error) {
	result := &v1.EphemeralResult{TaskId: taskID}
	var deadline time.Time
	err := s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if !oauthstore.HasScope(c, "devices:read") {
			return ErrUnavailable
		}
		task, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM mobile_phone_tasks WHERE id=? AND owner_id=? AND viewer_device_id=?`, taskID, c.Subject, c.DeviceID))
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				return ephemeral.ErrExpired
			}
			return err
		}
		if task.Cancelled || !nativeSessionLive(ctx, tx, task) || !s.originFamilyLive(ctx, tx, task) || s.chats.OriginAllowed(ctx, task.Owner, task.Conversation, task.Turn, task.Generation) != nil {
			return ephemeral.ErrExpired
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,category,expires_at FROM mobile_phone_observations WHERE task_id=? AND owner_id=? AND device_id=? ORDER BY id LIMIT 16`, taskID, c.Subject, task.Device)
		if err != nil {
			return err
		}
		defer rows.Close() //nolint:errcheck // Cleanup only; preserve the operation result.
		for rows.Next() {
			var id string
			var category v1.ObservationCategory
			var expiry int64
			if checkErr5 := rows.Scan(&id, &category, &expiry); checkErr5 != nil {
				return checkErr5
			}
			if expiry <= time.Now().UnixMilli() {
				return ephemeral.ErrExpired
			}
			name := "status"
			switch category {
			case v1.ObservationCategory_OBSERVATION_CATEGORY_LOCATION:
				name = "location"
			case v1.ObservationCategory_OBSERVATION_CATEGORY_NOTIFICATIONS:
				name = "notifications"
			}
			body, expires, stepErr4 := s.content.Get(id, s.binding(task.Owner, task.Device, task.ID, task.Destination, task.Consent, name))
			if stepErr4 != nil {
				return stepErr4
			}
			o := new(v1.Observation)
			stepErr4 = proto.Unmarshal(body, o)
			clear(body)
			if stepErr4 != nil {
				return ErrInvalid
			}
			result.Observations = append(result.Observations, o)
			if deadline.IsZero() || expires.Before(deadline) {
				deadline = expires
			}
		}
		if checkErr6 := rows.Err(); checkErr6 != nil {
			return checkErr6
		}
		if len(result.Observations) == 0 {
			return ephemeral.ErrExpired
		}
		result.ExpiresAt = timestamppb.New(deadline)
		return nil
	})
	return result, err
}

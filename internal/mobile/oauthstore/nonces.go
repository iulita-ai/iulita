package oauthstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"time"
)

// CreateNonce returns the raw nonce once. Only its keyed hash is persisted.
// The caller builds the bound challenge/signing_input and validates a native
// public-key proof before ConsumeNonce (BE16/BE17). No proof is inferred here.
func (s *Store) CreateNonce(ctx context.Context, owner, family, id, purpose string) (string, error) {
	if !canonicalID(owner) || !canonicalID(id) {
		return "", ErrInvalid
	}
	var nonce string
	err := s.WithRequest(ctx, func(ctx context.Context) error {
		f, err := s.familyIn(ctx, family)
		if err != nil || f.OwnerID != owner {
			return ErrNotFound
		}
		if s.live(ctx, f) != nil {
			return fositeInvalid()
		}
		if (f.Stage != Pending || purpose != "activate_device") && (f.Stage != Recovery || purpose != "recover_device") {
			return ErrInvalid
		}
		q, operationErr1 := s.transaction(ctx)
		if operationErr1 != nil {
			return operationErr1
		}
		var thumb string
		if checkErr1 := q.QueryRowContext(ctx, `SELECT thumbprint FROM oauth_devices WHERE owner_id=? AND id=?`, owner, f.DeviceID).Scan(&thumb); checkErr1 != nil {
			return checkErr1
		}
		b := make([]byte, 32)
		if _, err = rand.Read(b); err != nil {
			return err
		}
		nonce = base64.RawURLEncoding.EncodeToString(b)
		_, err = q.ExecContext(ctx, `INSERT INTO oauth_nonces(id,owner_id,device_id,family_id,purpose,key_thumbprint,nonce_hash,expires_at) VALUES(?,?,?,?,?,?,?,?)`, id, owner, f.DeviceID, f.ID, purpose, thumb, s.realm.digest("nonce", nonce), f.Deadline.Unix())
		return err
	})
	if err != nil {
		return "", err
	}
	return nonce, nil
}

// ConsumeNonce requires the surrounding activation/recovery transaction. It is
// an owned CAS primitive; public-key proof verification is not supplied by BE13.
func (s *Store) ConsumeNonce(ctx context.Context, owner, family, id, purpose, nonce string) error {
	q, err := s.transaction(ctx)
	if err != nil {
		return err
	}
	f, err := s.familyIn(ctx, family)
	if err != nil || f.OwnerID != owner {
		return ErrNotFound
	}
	if s.live(ctx, f) != nil {
		return fositeInvalid()
	}
	res, err := q.ExecContext(ctx, `UPDATE oauth_nonces SET consumed=1 WHERE owner_id=? AND family_id=? AND device_id=? AND id=? AND purpose=? AND nonce_hash=? AND consumed=0 AND expires_at>?`, owner, family, f.DeviceID, id, purpose, s.realm.digest("nonce", nonce), time.Now().Unix())
	if err != nil {
		return err
	}
	n, operationErr2 := res.RowsAffected()
	if operationErr2 != nil {
		return operationErr2
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

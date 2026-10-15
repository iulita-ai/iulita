package oauthstore

var lifecycleSchema = []string{
	`ALTER TABLE oauth_devices ADD COLUMN public_jwk TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE oauth_devices ADD COLUMN label TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE oauth_devices ADD COLUMN pending_expires_at INTEGER NOT NULL DEFAULT 0`,
	`UPDATE oauth_devices SET pending_expires_at=COALESCE((SELECT MIN(deadline) FROM oauth_families f WHERE f.owner_id=oauth_devices.owner_id AND f.device_id=oauth_devices.id AND f.stage='pending_activation'),0) WHERE activated=0`,
	`ALTER TABLE oauth_owners ADD COLUMN account_stamp BLOB`,
	`CREATE TABLE oauth_completions(family_id TEXT PRIMARY KEY REFERENCES oauth_families(id),operation_id TEXT NOT NULL,digest BLOB NOT NULL,kind TEXT NOT NULL,completed_at INTEGER NOT NULL)`,
	`CREATE TABLE oauth_device_revocations(owner_id TEXT NOT NULL REFERENCES oauth_owners(id),client_id TEXT NOT NULL,operation_id TEXT NOT NULL,device_id TEXT NOT NULL,PRIMARY KEY(owner_id,client_id,operation_id))`,
	`CREATE INDEX oauth_family_device ON oauth_families(owner_id,device_id,revoked)`,
	`ALTER TABLE oauth_schema RENAME TO oauth_schema_v2`,
	`CREATE TABLE oauth_schema(version INTEGER PRIMARY KEY CHECK(version=3))`,
	`INSERT INTO oauth_schema VALUES(3)`,
	`DROP TABLE oauth_schema_v2`,
}

func (s *Store) migrateLifecycle() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	var version int
	if checkErr1 := tx.QueryRow(`SELECT MAX(version) FROM oauth_schema`).Scan(&version); checkErr1 != nil {
		return checkErr1
	}
	if version == 3 {
		return tx.Commit()
	}
	if version != 2 {
		return ErrInvalid
	}
	for _, q := range lifecycleSchema {
		if _, err = tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

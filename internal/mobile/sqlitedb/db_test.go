package sqlitedb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestReadOnlyPoolCannotWriteOnAnyConnection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "authority.sqlite")
	writer, err := Open(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err = writer.ExecContext(ctx, `CREATE TABLE authority(epoch INTEGER); INSERT INTO authority VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	connections := make([]*sql.Conn, 0, reader.Stats().MaxOpenConnections)
	defer func() {
		for _, connection := range connections {
			connection.Close()
		}
	}()
	for range reader.Stats().MaxOpenConnections {
		connection, connErr := reader.Conn(ctx)
		if connErr != nil {
			t.Fatal(connErr)
		}
		connections = append(connections, connection)
		var epoch, queryOnly int
		if connErr = connection.QueryRowContext(ctx, `SELECT epoch FROM authority`).Scan(&epoch); connErr != nil || epoch != 1 {
			t.Fatal("read failed", connErr)
		}
		if connErr = connection.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&queryOnly); connErr != nil || queryOnly != 1 {
			t.Fatal("query_only not set on pooled connection", connErr)
		}
		if _, connErr = connection.ExecContext(ctx, `UPDATE authority SET epoch=2`); connErr == nil {
			t.Fatal("read-only connection performed a write")
		}
	}
	var epoch int
	if err = writer.QueryRowContext(ctx, `SELECT epoch FROM authority`).Scan(&epoch); err != nil || epoch != 1 {
		t.Fatal("read-only pool changed authority", err)
	}
}
func TestReadOnlyOpenNeverCreatesMissingStore(t *testing.T) {
	if database, err := OpenReadOnly(context.Background(), filepath.Join(t.TempDir(), "missing.sqlite")); err == nil {
		database.Close()
		t.Fatal("read-only opener created a store")
	}
}

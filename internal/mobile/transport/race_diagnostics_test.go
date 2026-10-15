package transport

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
)

// Only authority metadata is inspected on test failure, never phone content.
func phoneFailureState(t *testing.T, h *harness, command *v1.Command) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var result strings.Builder
	for index, path := range h.paths {
		db, err := sqlitedb.OpenReadOnly(ctx, path)
		if err != nil {
			fmt.Fprintf(&result, "db%d: %v;", index, err)
			continue
		}
		queries := []string{`SELECT t.state,t.cancellation_requested,t.reason,s.state,s.expires_at-?,c.expires_at-?,f.revoked FROM mobile_phone_tasks t JOIN mobile_native_sessions s ON s.id=t.session_id JOIN mobile_device_streams c ON c.device_id=t.device_id JOIN oauth_families f ON f.id=t.caller_family_id WHERE t.id=?`, `SELECT COUNT(*),MIN(expires_at)-? FROM mobile_phone_observations WHERE task_id=?`}
		args := [][]any{{time.Now().UnixMilli(), time.Now().UnixMilli(), command.TaskId}, {time.Now().UnixMilli(), command.TaskId}}
		if index == 0 {
			queries = []string{`SELECT state,cancellation_requested,outcome_reason FROM mobile_turns WHERE id=?`, `SELECT state,effect FROM mobile_tool_operations WHERE turn_id=?`}
			args = [][]any{{command.Origin.TurnId}, {command.Origin.TurnId}}
		}
		for queryIndex, query := range queries {
			rows, queryErr := db.QueryContext(ctx, query, args[queryIndex]...)
			if queryErr != nil {
				fmt.Fprintf(&result, "db%d/q%d: %v;", index, queryIndex, queryErr)
				continue
			}
			appendFailureRows(&result, rows, index, queryIndex)
		}
		db.Close()
	}
	return result.String()
}
func appendFailureRows(result *strings.Builder, rows *sql.Rows, dbIndex, queryIndex int) {
	defer rows.Close()
	columns, _ := rows.Columns()
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for index := range values {
			targets[index] = &values[index]
		}
		if err := rows.Scan(targets...); err != nil {
			fmt.Fprint(result, err)
			return
		}
		fmt.Fprintf(result, "db%d/q%d=%v;", dbIndex, queryIndex, values)
	}
	if err := rows.Err(); err != nil {
		fmt.Fprint(result, err)
	}
}

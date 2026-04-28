package db

import (
	"context"
	"fmt"
	"time"
)

// ExplainTimeoutMs is the server-side optimizer timeout we set on the
// EXPLAIN session. The client also enforces a longer wall-clock
// deadline via the supplied context.
const ExplainTimeoutMs = 2000

// ExplainJSON runs EXPLAIN FORMAT=JSON on a dedicated connection
// configured for read-only execution. The connection's session is
// reset and released back to the pool on return. schema may be empty.
//
// The caller is responsible for verb safety (see internal/explain
// package). This method makes no attempt to validate sql; it relies
// on transaction_read_only=ON to neutralise any side-effecting
// statement that slips through.
func (m *MySQLDB) ExplainJSON(ctx context.Context, sql, schema string) (string, error) {
	if sql == "" {
		return "", fmt.Errorf("empty sql")
	}

	conn, err := m.db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("acquire explain connection: %w", err)
	}
	defer conn.Close()

	// Session safety belt and braces: read-only + bounded optimizer time.
	if _, err := conn.ExecContext(ctx, "SET SESSION transaction_read_only=ON"); err != nil {
		return "", fmt.Errorf("set transaction_read_only: %w", err)
	}
	if _, err := conn.ExecContext(ctx,
		fmt.Sprintf("SET SESSION MAX_EXECUTION_TIME=%d", ExplainTimeoutMs)); err != nil {
		// MariaDB and very old MySQL do not support this variable;
		// rely on the context deadline instead.
	}

	if schema != "" {
		// USE rejects unknown schemas; we tolerate failure here so an
		// example whose schema was dropped still falls through to a
		// best-effort EXPLAIN under the connection's default schema.
		_, _ = conn.ExecContext(ctx, "USE `"+escapeIdent(schema)+"`")
	}

	// Run with a deadline of max(ctx-deadline, 5s) so a server-side
	// MAX_EXECUTION_TIME mismatch cannot hang us indefinitely.
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}

	var planJSON string
	row := conn.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+sql)
	if err := row.Scan(&planJSON); err != nil {
		return "", fmt.Errorf("explain: %w", err)
	}
	return planJSON, nil
}

// escapeIdent escapes a backtick-quoted identifier by doubling
// backticks. We never accept backslash escapes because mysql does
// not interpret them inside backticks.
func escapeIdent(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			out = append(out, '`', '`')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

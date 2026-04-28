//go:build mdl_smoke

// Live smoke test for the MDL pipeline. Requires a MySQL 8.0
// container at 127.0.0.1:13306 with root/test credentials and a
// pre-set up `mdltest.orders` table under MDL contention. Run via
//
//   go test -tags=mdl_smoke -run TestMDLSmoke ./internal/insights/...
//
// Excluded from the default build by the build tag so CI doesn't
// require the container.
package insights

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/stretchr/testify/require"
)

func TestMDLSmoke(t *testing.T) {
	dsn := "root:test@tcp(127.0.0.1:13306)/?parseTime=true&timeout=5s&readTimeout=10s"
	mysql, err := db.NewMySQL(dsn)
	require.NoError(t, err)
	defer mysql.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	locks, err := mysql.MetadataLocks(ctx)
	require.NoError(t, err)
	t.Logf("MetadataLocks returned %d rows", len(locks))

	snap := db.Snapshot{MetadataLocks: locks}
	mdl := BuildMDL(snap)
	for _, q := range mdl.Tables {
		t.Logf("table %s.%s — %d granted, %d pending",
			q.Schema, q.Name, len(q.Granted), len(q.Pending))
		for i, p := range q.Pending {
			rank, total, _ := q.PositionOf(p.PID)
			fmt.Printf("    QUEUE #%d/%d pid=%d %s age=%ds info=%q\n",
				rank, total, p.PID, p.LockType, p.WaitSeconds, p.Query)
			_ = i
		}
		for _, h := range q.Granted {
			fmt.Printf("    HOLDER pid=%d %s duration=%s age=%ds info=%q\n",
				h.PID, h.LockType, h.LockDuration, h.WaitSeconds, h.Query)
		}
		// Position of any waiter and its blockers.
		if len(q.Pending) > 0 {
			head := q.Pending[0]
			rank, total, ok := q.PositionOf(head.PID)
			t.Logf("PositionOf head waiter pid=%d → rank=%d total=%d ok=%v",
				head.PID, rank, total, ok)
			for _, b := range q.BlockersOf(head.PID) {
				fmt.Printf("    BLOCKER pid=%d %s\n", b.PID, b.LockType)
			}
		}
	}
}

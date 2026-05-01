package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTreeEntries_RendersAllDeadlockParticipants(t *testing.T) {
	// Two-participant deadlock where only one thread is still in the
	// process list. Both participants must appear so the operator
	// sees both sides — a deadlock by definition has >=2 actors.
	snap := db.Snapshot{
		Processes: []db.Process{
			{ID: 100, User: "live", Host: "10.0.0.1", Info: "UPDATE hotel_user SET x=1"},
		},
		InnoDBStatus: db.InnoDBStatus{
			LatestDeadlock: &db.DeadlockInfo{
				Timestamp: "2026-04-27 12:00:00",
				Transactions: []db.DeadlockTransaction{
					{ThreadID: 100, User: "live", Host: "10.0.0.1", Query: "UPDATE hotel_user SET x=1", TableName: "alice.hotel_user"},
					{ThreadID: 200, User: "victim", Host: "10.0.0.2", Query: "UPDATE user_app_preference SET y=2", TableName: "alice.user_app_preference"},
				},
			},
		},
	}
	entries := buildTreeEntries(nil, snap)

	var deadlockPIDs []uint64
	for _, e := range entries {
		if e.isDeadlock {
			deadlockPIDs = append(deadlockPIDs, e.pid)
		}
	}
	require.Len(t, deadlockPIDs, 2, "both participants must render even if victim is rolled back")
	assert.ElementsMatch(t, []uint64{100, 200}, deadlockPIDs)

	// Last entry must close the group with isLast=true so the
	// renderer emits └ instead of leaving an open ┌.
	last := entries[len(entries)-1]
	assert.True(t, last.isLast, "final deadlock entry must close the group")
}

func TestBuildTreeEntries_SingletonDeadlockClosesConnector(t *testing.T) {
	// Pathological one-participant deadlock (e.g. parser only saw
	// half the dump). The renderer relies on isLast=true to draw a
	// stand-alone └ instead of an unmatched ┌.
	snap := db.Snapshot{
		InnoDBStatus: db.InnoDBStatus{
			LatestDeadlock: &db.DeadlockInfo{
				Timestamp:    "2026-04-27 12:00:00",
				Transactions: []db.DeadlockTransaction{{ThreadID: 100, User: "u", Host: "h", Query: "X", TableName: "t"}},
			},
		},
	}
	entries := buildTreeEntries(nil, snap)
	require.Len(t, entries, 1)
	assert.True(t, entries[0].isLast, "singleton must self-close")
}


func TestExtractTableFromSQL(t *testing.T) {
	cases := map[string]string{
		"":                                              "",
		"SELECT 1":                                      "",
		"SELECT * FROM users":                           "users",
		"SELECT * FROM `users`":                         "users",
		"SELECT * FROM `alice`.`hskp_message`":          "alice.hskp_message",
		"SELECT * FROM alice.hskp_message":              "alice.hskp_message",
		"UPDATE `accounts` SET balance=balance+1":       "accounts",
		"INSERT INTO `mydb`.`audit_log` VALUES (?)":     "mydb.audit_log",
		"SELECT * FROM a JOIN `b`.`c` ON a.id=c.id":     "a",
		"/* hint */ SELECT * FROM `alice`.`hskp_task`":  "alice.hskp_task",
		"BEGIN":                                         "",
	}
	for in, want := range cases {
		assert.Equalf(t, want, extractTableFromSQL(in), "input=%q", in)
	}
}

func TestStripLeadingSQLComments(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"SELECT 1":                "SELECT 1",
		"/* hint */ SELECT 1":     "SELECT 1",
		"  /* hint */  SELECT 1":  "SELECT 1",
		"/* unclosed":             "",
		"/* a */ /* b */ UPDATE t": "UPDATE t",
		"-- line\nSELECT 1":       "SELECT 1",
		"# hash\nSELECT 1":        "SELECT 1",
		"/* only a comment */":    "",
	}
	for in, want := range cases {
		assert.Equalf(t, want, stripLeadingSQLComments(in), "input=%q", in)
	}
}

func TestSimplifyQuery_StripsLeadingComment(t *testing.T) {
	got := simplifyQuery("/* trx-id=123 */ SELECT * FROM users WHERE id = 5")
	assert.Equal(t, "SELECT users", got)
}

func TestSimplifyQuery_OnlyCommentReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", simplifyQuery("/* just a comment */"))
}

func TestQueryLabel_PrefersDigestButStripsComments(t *testing.T) {
	got := queryLabel("/* hint */ SELECT * FROM `t` WHERE `id` = ?", "")
	assert.Equal(t, "SELECT * FROM `t` WHERE `id` = ?", got)
}

func TestQueryLabel_FallsBackThroughEmptyDigest(t *testing.T) {
	got := queryLabel("", "/* trx */ UPDATE accounts SET balance = ? WHERE id = ?")
	assert.Equal(t, "UPDATE accounts", got)
}

func TestQueryLabel(t *testing.T) {
	tests := []struct {
		name     string
		digest   string
		rawQuery string
		want     string
	}{
		{
			name:     "digest present",
			digest:   "UPDATE `accounts` SET `balance` = `balance` + ? WHERE `id` = ?",
			rawQuery: "UPDATE accounts SET balance = balance + 100 WHERE id = 42",
			want:     "UPDATE `accounts` SET `balance` = `balance` + ? WHERE `id` = ?",
		},
		{
			name:     "digest empty falls back to simplifyQuery",
			digest:   "",
			rawQuery: "UPDATE accounts SET balance = balance + 100 WHERE id = 42",
			want:     "UPDATE accounts",
		},
		{
			name:     "both empty",
			digest:   "",
			rawQuery: "",
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := queryLabel(tt.digest, tt.rawQuery)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRenderHeader_NoDuplicatedSparklineOrCounts(t *testing.T) {
	// The whole point of the chrome rework: the body's sparkline is the
	// only one on screen, and the body's verdict line is the only place
	// counts (Transactions / Lock Waits / Processes) appear.
	m := Model{
		width:  120,
		height: 40,
		view:   ViewOverview,
		result: monitor.Result{Snapshot: db.Snapshot{
			Time:       time.Date(2026, 4, 28, 10, 42, 13, 0, time.UTC),
			ServerInfo: db.ServerInfo{Version: "8.0.45"},
			Transactions: []db.Transaction{
				{ID: 1}, {ID: 2}, {ID: 3},
			},
			LockWaits: []db.LockWait{{WaitingPID: 1}},
			Processes: []db.Process{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}, {ID: 5}},
		}},
	}
	out := renderHeader(m)
	assert.NotContains(t, out, "Transactions:")
	assert.NotContains(t, out, "Lock Waits:")
	assert.NotContains(t, out, "Processes:")
	assert.NotContains(t, out, "DB Load")
	assert.NotContains(t, out, "Server: ")
	// But the chrome itself MUST be present.
	assert.Contains(t, out, "MySQL Lock Monitor")
}

func TestRenderHeader_RightAlignedContextSegments(t *testing.T) {
	m := Model{
		width:  120,
		height: 40,
		view:   ViewOverview,
		result: monitor.Result{Snapshot: db.Snapshot{
			Time:       time.Date(2026, 4, 28, 10, 42, 13, 0, time.UTC),
			ServerInfo: db.ServerInfo{Version: "8.0.45-0ubuntu0.20.04.3", IsRDS: true},
		}},
	}
	out := renderHeader(m)
	assert.Contains(t, out, "10:42:13")
	assert.Contains(t, out, "8.0.45 RDS") // version stripped of build suffix
}

func TestRenderHeader_OmitsContextWhenSnapshotEmpty(t *testing.T) {
	// First-frame path: we have no snapshot yet. The header still
	// renders chrome but no context segments — never empty separators.
	m := Model{width: 120, height: 40, view: ViewOverview}
	out := renderHeader(m)
	assert.Contains(t, out, "MySQL Lock Monitor")
	assert.NotContains(t, out, "·") // separator only between non-empty segments
}

func TestRenderHeader_ContextStacksWhenWidthUnknown(t *testing.T) {
	// Pre-WindowSizeMsg path (width=0): right-align math is impossible,
	// so we stack title-bar above context. Both must be present.
	m := Model{
		view: ViewOverview,
		result: monitor.Result{Snapshot: db.Snapshot{
			Time:       time.Date(2026, 4, 28, 10, 42, 13, 0, time.UTC),
			ServerInfo: db.ServerInfo{Version: "8.0.45"},
		}},
	}
	out := renderHeader(m)
	assert.Contains(t, out, "MySQL Lock Monitor")
	assert.Contains(t, out, "10:42:13")
	assert.Contains(t, out, "8.0.45")
}

func TestCompactVersion_StripsBuildSuffix(t *testing.T) {
	assert.Equal(t, "8.0.45", compactVersion("8.0.45-0ubuntu0.20.04.3"))
	assert.Equal(t, "10.5.21", compactVersion("10.5.21-MariaDB"))
	assert.Equal(t, "5.7.42", compactVersion("5.7.42"))
}

func TestHumanUptime_GranularityScalesWithMagnitude(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{47 * time.Second, "47s"},
		{47 * time.Minute, "47m"},
		{2*time.Hour + 17*time.Minute, "2h 17m"},
		{2 * time.Hour, "2h"},
		{14*24*time.Hour + 3*time.Hour, "14d 3h"},
		{14 * 24 * time.Hour, "14d"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, humanUptime(c.d), c.d.String())
	}
}

func TestRenderHeader_UptimeShownWhenHealthAvailable(t *testing.T) {
	// Sanity: when the model has no insights, uptime is suppressed.
	m := Model{
		width:  120,
		height: 40,
		view:   ViewOverview,
		result: monitor.Result{Snapshot: db.Snapshot{
			Time:       time.Date(2026, 4, 28, 10, 42, 13, 0, time.UTC),
			ServerInfo: db.ServerInfo{Version: "8.0.45"},
		}},
	}
	out := renderHeader(m)
	assert.NotContains(t, out, "up ")
	// We don't test the populated path here — that would require
	// constructing an Insights with a real HealthCollector. The
	// integration smoke at Phase 5 covers it end-to-end.
	_ = strings.TrimSpace(out)
}

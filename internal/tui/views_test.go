package tui

import (
	"testing"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
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

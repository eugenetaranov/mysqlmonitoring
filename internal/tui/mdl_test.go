package tui

import (
	"strings"
	"testing"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/insights"
	"github.com/eugenetaranov/mysqlmonitoring/internal/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mdlModel(locks ...db.MetadataLock) Model {
	return Model{
		width:  120,
		height: 40,
		view:   ViewMDL,
		result: monitor.Result{Snapshot: db.Snapshot{
			MetadataLocks: locks,
		}},
	}
}

func mdlRow(schema, table, status, lockType string, pid uint64, age int64) db.MetadataLock {
	return db.MetadataLock{
		ObjectType:   "TABLE",
		ObjectSchema: schema,
		ObjectName:   table,
		LockStatus:   status,
		LockType:     lockType,
		PID:          pid,
		User:         "app_rw",
		Host:         "10.0.0.1:51234",
		TimeSeconds:  age,
		Info:         "ALTER TABLE " + table + " ADD INDEX foo(col)",
	}
}

func TestRenderMDL_EmptyShowsHelpfulMessage(t *testing.T) {
	m := mdlModel()
	out := renderMDL(m)
	assert.Contains(t, out, "MDL queue")
	assert.Contains(t, out, "No metadata locks observed")
}

func TestRenderMDLList_ShowsTablesSortedByQueueDepth(t *testing.T) {
	m := mdlModel(
		mdlRow("shop", "orders", "PENDING", "EXCLUSIVE", 100, 240),
		mdlRow("shop", "orders", "PENDING", "SHARED_WRITE", 101, 60),
		mdlRow("shop", "orders", "GRANTED", "SHARED_READ", 200, 60),
		mdlRow("auth", "sessions", "PENDING", "INTENTION_EXCLUSIVE", 300, 5),
	)
	out := renderMDLList(m, mustBuildMDL(m))
	assert.Contains(t, out, "shop.orders")
	assert.Contains(t, out, "auth.sessions")
	// Order: shop.orders (depth 2) before auth.sessions (depth 1).
	assert.Less(t, strings.Index(out, "shop.orders"), strings.Index(out, "auth.sessions"))
	// Queue depth column shows 2 for orders.
	assert.Regexp(t, `shop\.orders\s+2\s+1`, out)
}

func TestRenderMDLDetail_QueueAndHoldersBothPresent(t *testing.T) {
	m := mdlModel(
		mdlRow("shop", "orders", "GRANTED", "SHARED_READ", 200, 600), // 10m holder
		mdlRow("shop", "orders", "PENDING", "EXCLUSIVE", 100, 240),
		mdlRow("shop", "orders", "PENDING", "SHARED_WRITE", 101, 60),
	)
	m.mdlMode = MDLModeDetail
	m.mdlTableSchema = "shop"
	m.mdlTableName = "orders"

	out := renderMDLDetail(m, mustBuildMDL(m))
	assert.Contains(t, out, "MDL queue · shop.orders")
	assert.Contains(t, out, "QUEUE")
	assert.Contains(t, out, "HOLDERS")
	assert.Contains(t, out, "EXCLUSIVE")
	assert.Contains(t, out, "SHARED_READ")
	// Position 1 (head of queue) for the EXCLUSIVE waiter at PID 100.
	assert.Regexp(t, `1\s+4m\s+100`, out)
}

func TestRenderMDLDetail_BlockerFilterShowsConflictingHoldersOnly(t *testing.T) {
	m := mdlModel(
		// Three holders with different lock types.
		mdlRow("shop", "orders", "GRANTED", "SHARED_READ", 200, 600),
		mdlRow("shop", "orders", "GRANTED", "SHARED_WRITE", 201, 600),
		mdlRow("shop", "orders", "GRANTED", "EXCLUSIVE", 202, 600),
		// One waiter requesting SHARED_READ — only EXCLUSIVE blocks.
		mdlRow("shop", "orders", "PENDING", "SHARED_READ", 100, 60),
	)
	m.mdlMode = MDLModeDetail
	m.mdlTableSchema = "shop"
	m.mdlTableName = "orders"
	m.mdlBlockerFilter = true

	out := renderMDLDetail(m, mustBuildMDL(m))
	// Only PID 202 (EXCLUSIVE holder) should appear in HOLDERS.
	assert.Contains(t, out, "202")
	assert.NotContains(t, out, "200 ") // SHARED_READ holder, no longer in HOLDERS panel
	assert.NotContains(t, out, "201 ") // SHARED_WRITE holder, no longer in HOLDERS panel
}

func TestRenderMDLDetail_MissingTableShowsBackHint(t *testing.T) {
	m := mdlModel(
		mdlRow("shop", "orders", "PENDING", "EXCLUSIVE", 100, 60),
	)
	m.mdlMode = MDLModeDetail
	m.mdlTableSchema = "missing"
	m.mdlTableName = "table"
	out := renderMDLDetail(m, mustBuildMDL(m))
	assert.Contains(t, out, "No metadata locks on this table")
}

func TestFormatLockTypeBuckets_SortedByCountDesc(t *testing.T) {
	got := formatLockTypeBuckets(map[string]int{
		"SHARED_READ":  38,
		"SHARED_WRITE": 6,
		"EXCLUSIVE":    1,
	})
	assert.Contains(t, got, "38×SHARED_READ")
	// Largest bucket appears first.
	assert.Less(t, strings.Index(got, "38×SHARED_READ"), strings.Index(got, "6×SHARED_WRITE"))
}

func TestFormatLockTypeBuckets_EmptyShowsDash(t *testing.T) {
	assert.Equal(t, "—", formatLockTypeBuckets(map[string]int{}))
}

func TestRenderMDLDetail_ShowsQueuePosition(t *testing.T) {
	m := mdlModel(
		mdlRow("a", "t", "PENDING", "EXCLUSIVE", 1, 100),
		mdlRow("a", "t", "PENDING", "SHARED_WRITE", 2, 80),
		mdlRow("a", "t", "PENDING", "SHARED_READ", 3, 5),
	)
	m.mdlMode = MDLModeDetail
	m.mdlTableSchema = "a"
	m.mdlTableName = "t"
	out := renderMDLDetail(m, mustBuildMDL(m))
	// Header summary states 3 waiters.
	assert.Contains(t, out, "3 waiters")
	// All three waiters appear with their PIDs.
	assert.Contains(t, out, " 1 ") // rank 1
	assert.Contains(t, out, " 2 ") // rank 2
	assert.Contains(t, out, " 3 ") // rank 3
}

func TestRenderMDL_DispatchesToCorrectMode(t *testing.T) {
	m := mdlModel(mdlRow("a", "t", "PENDING", "SHARED_READ", 1, 5))
	m.mdlMode = MDLModeList
	listOut := renderMDL(m)
	assert.Contains(t, listOut, "hottest tables")

	m.mdlMode = MDLModeDetail
	m.mdlTableSchema = "a"
	m.mdlTableName = "t"
	detailOut := renderMDL(m)
	assert.Contains(t, detailOut, "MDL queue · a.t")
}

func TestMDLTabInTabBar(t *testing.T) {
	m := mdlModel()
	bar := renderTabBar(m)
	assert.Contains(t, bar, "M")
	assert.Contains(t, bar, "MDL")
}

func TestHandleMDLListKey_EnterEntersDetail(t *testing.T) {
	m := mdlModel(
		mdlRow("shop", "orders", "PENDING", "EXCLUSIVE", 100, 60),
	)
	m.mdlMode = MDLModeList

	got, _ := m.handleMDLKey("enter")
	gm := got.(Model)
	assert.Equal(t, MDLModeDetail, gm.mdlMode)
	assert.Equal(t, "shop", gm.mdlTableSchema)
	assert.Equal(t, "orders", gm.mdlTableName)
}

func TestHandleMDLDetailKey_BToggleBlockerFilter(t *testing.T) {
	m := mdlModel(
		mdlRow("a", "t", "GRANTED", "EXCLUSIVE", 200, 60),
		mdlRow("a", "t", "PENDING", "SHARED_READ", 100, 60),
	)
	m.mdlMode = MDLModeDetail
	m.mdlTableSchema = "a"
	m.mdlTableName = "t"

	got, _ := m.handleMDLKey("B")
	require.True(t, got.(Model).mdlBlockerFilter)

	got, _ = got.(Model).handleMDLKey("B")
	assert.False(t, got.(Model).mdlBlockerFilter)
}

func TestHandleMDLDetailKey_DownNavigatesQueue(t *testing.T) {
	m := mdlModel(
		mdlRow("a", "t", "PENDING", "EXCLUSIVE", 1, 100),
		mdlRow("a", "t", "PENDING", "SHARED_WRITE", 2, 50),
		mdlRow("a", "t", "PENDING", "SHARED_READ", 3, 5),
	)
	m.mdlMode = MDLModeDetail
	m.mdlTableSchema = "a"
	m.mdlTableName = "t"

	got, _ := m.handleMDLKey("down")
	assert.Equal(t, 1, got.(Model).mdlQueueCursor)

	got, _ = got.(Model).handleMDLKey("down")
	assert.Equal(t, 2, got.(Model).mdlQueueCursor)

	// Bounds: shouldn't go past last.
	got, _ = got.(Model).handleMDLKey("down")
	assert.Equal(t, 2, got.(Model).mdlQueueCursor)

	// G jumps to last.
	got, _ = m.handleMDLKey("G")
	assert.Equal(t, 2, got.(Model).mdlQueueCursor)
}

func mustBuildMDL(m Model) insights.MDLBreakdown {
	return insights.BuildMDL(m.result.Snapshot)
}

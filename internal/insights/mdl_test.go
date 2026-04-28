package insights

import (
	"testing"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func snap(locks ...db.MetadataLock) db.Snapshot {
	return db.Snapshot{MetadataLocks: locks}
}

func mdl(schema, table, status, lockType string, pid uint64, age int64) db.MetadataLock {
	return db.MetadataLock{
		ObjectType:   "TABLE",
		ObjectSchema: schema,
		ObjectName:   table,
		LockStatus:   status,
		LockType:     lockType,
		PID:          pid,
		TimeSeconds:  age,
	}
}

func TestBuildMDL_Empty(t *testing.T) {
	got := BuildMDL(snap())
	assert.Empty(t, got.Tables)
}

func TestBuildMDL_SkipsNonTableEntries(t *testing.T) {
	// SCHEMA-scope MDL rows shouldn't show up in a table view.
	got := BuildMDL(db.Snapshot{MetadataLocks: []db.MetadataLock{
		{ObjectType: "SCHEMA", ObjectSchema: "shop", LockStatus: "GRANTED", LockType: "INTENTION_EXCLUSIVE"},
		mdl("shop", "orders", "GRANTED", "SHARED_READ", 100, 5),
	}})
	require.Len(t, got.Tables, 1)
	assert.Equal(t, "orders", got.Tables[0].Name)
}

func TestBuildMDL_GroupsByTableAndSplitsHolderVsWaiter(t *testing.T) {
	got := BuildMDL(snap(
		mdl("shop", "orders", "GRANTED", "SHARED_READ", 100, 30),
		mdl("shop", "orders", "GRANTED", "SHARED_READ", 101, 60),
		mdl("shop", "orders", "PENDING", "EXCLUSIVE", 200, 47),
		mdl("shop", "orders", "PENDING", "SHARED_WRITE", 201, 12),
		mdl("auth", "sessions", "PENDING", "INTENTION_EXCLUSIVE", 300, 5),
	))
	require.Len(t, got.Tables, 2)

	// Sort: shop.orders has 2 pending, auth.sessions has 1.
	assert.Equal(t, "orders", got.Tables[0].Name)
	assert.Equal(t, "sessions", got.Tables[1].Name)

	q := got.Tables[0]
	require.Len(t, q.Granted, 2)
	require.Len(t, q.Pending, 2)
	// Pending sorted oldest-first.
	assert.Equal(t, uint64(200), q.Pending[0].PID)
	assert.Equal(t, uint64(201), q.Pending[1].PID)
	// Granted also sorted by age desc.
	assert.Equal(t, uint64(101), q.Granted[0].PID)

	// ByLockType counts pending entries per LOCK_TYPE.
	assert.Equal(t, 1, q.ByLockType["EXCLUSIVE"])
	assert.Equal(t, 1, q.ByLockType["SHARED_WRITE"])
	assert.Equal(t, 0, q.ByLockType["SHARED_READ"]) // granted, not pending
}

func TestBuildMDL_SortByQueueDepthThenOldestPending(t *testing.T) {
	// Two tables with the same pending depth; the one with the older
	// pending wait should come first.
	got := BuildMDL(snap(
		mdl("a", "t1", "PENDING", "SHARED_READ", 100, 10),
		mdl("a", "t1", "PENDING", "SHARED_READ", 101, 20),
		mdl("a", "t2", "PENDING", "SHARED_READ", 200, 100),
		mdl("a", "t2", "PENDING", "SHARED_READ", 201, 5),
	))
	require.Len(t, got.Tables, 2)
	assert.Equal(t, "t2", got.Tables[0].Name) // older pending
	assert.Equal(t, "t1", got.Tables[1].Name)
}

func TestPositionOf_FoundAndMissing(t *testing.T) {
	got := BuildMDL(snap(
		mdl("a", "t", "PENDING", "EXCLUSIVE", 1, 100),
		mdl("a", "t", "PENDING", "SHARED_WRITE", 2, 50),
		mdl("a", "t", "PENDING", "SHARED_READ", 3, 5),
	))
	q := got.Find("a", "t")
	require.NotNil(t, q)

	rank, total, ok := q.PositionOf(2)
	assert.True(t, ok)
	assert.Equal(t, 2, rank)
	assert.Equal(t, 3, total)

	rank, total, ok = q.PositionOf(99)
	assert.False(t, ok)
	assert.Equal(t, 0, rank)
	assert.Equal(t, 3, total)
}

func TestPositionOf_HeadOfQueue(t *testing.T) {
	got := BuildMDL(snap(
		mdl("a", "t", "PENDING", "EXCLUSIVE", 1, 100),
	))
	q := got.Find("a", "t")
	rank, total, ok := q.PositionOf(1)
	assert.True(t, ok)
	assert.Equal(t, 1, rank)
	assert.Equal(t, 1, total)
}

func TestBlockersOf_ExclusiveSeesEveryHolder(t *testing.T) {
	got := BuildMDL(snap(
		mdl("a", "t", "GRANTED", "SHARED_READ", 10, 60),
		mdl("a", "t", "GRANTED", "SHARED_WRITE", 11, 60),
		mdl("a", "t", "GRANTED", "INTENTION_EXCLUSIVE", 12, 60),
		mdl("a", "t", "PENDING", "EXCLUSIVE", 1, 100),
	))
	q := got.Find("a", "t")
	require.NotNil(t, q)
	blockers := q.BlockersOf(1)
	require.Len(t, blockers, 3)
}

func TestBlockersOf_SharedReadOnlyBlockedByExclusiveTypes(t *testing.T) {
	got := BuildMDL(snap(
		mdl("a", "t", "GRANTED", "SHARED_READ", 10, 60),
		mdl("a", "t", "GRANTED", "SHARED_WRITE", 11, 60),
		mdl("a", "t", "GRANTED", "EXCLUSIVE", 12, 60),
		mdl("a", "t", "PENDING", "SHARED_READ", 1, 100),
	))
	q := got.Find("a", "t")
	blockers := q.BlockersOf(1)
	require.Len(t, blockers, 1)
	assert.Equal(t, "EXCLUSIVE", blockers[0].LockType)
}

func TestBlockersOf_PIDNotPendingReturnsNil(t *testing.T) {
	got := BuildMDL(snap(
		mdl("a", "t", "GRANTED", "SHARED_READ", 10, 60),
	))
	q := got.Find("a", "t")
	require.NotNil(t, q)
	assert.Nil(t, q.BlockersOf(99))
}

func TestBlockersOf_UnknownLockTypeIsConservative(t *testing.T) {
	got := BuildMDL(snap(
		mdl("a", "t", "GRANTED", "SHARED_READ", 10, 60),
		mdl("a", "t", "PENDING", "WEIRD_NEW_TYPE_FROM_FUTURE_MYSQL", 1, 100),
	))
	q := got.Find("a", "t")
	blockers := q.BlockersOf(1)
	// Conservative: unknown waiter type → assume holder blocks.
	require.Len(t, blockers, 1)
}

func TestFind_MissingTable(t *testing.T) {
	got := BuildMDL(snap(
		mdl("a", "t", "PENDING", "SHARED_READ", 1, 100),
	))
	assert.Nil(t, got.Find("a", "missing"))
	assert.Nil(t, got.Find("missing", "t"))
}

func TestBuildMDL_VictimAndOtherTransientStatesDropped(t *testing.T) {
	got := BuildMDL(snap(
		mdl("a", "t", "GRANTED", "SHARED_READ", 10, 60),
		mdl("a", "t", "VICTIM", "EXCLUSIVE", 11, 1),
		mdl("a", "t", "TIMEOUT", "EXCLUSIVE", 12, 1),
	))
	q := got.Find("a", "t")
	require.NotNil(t, q)
	assert.Len(t, q.Granted, 1)
	assert.Len(t, q.Pending, 0)
}

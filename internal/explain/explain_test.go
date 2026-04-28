package explain

import (
	"context"
	"errors"
	"testing"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSource struct {
	examples map[string]db.Example
	plans    []string
	idx      int
	err      error
}

func (f *fakeSource) RecentExample(_ context.Context, digest string) (db.Example, error) {
	if f.err != nil {
		return db.Example{}, f.err
	}
	return f.examples[digest], nil
}

func (f *fakeSource) ExplainJSON(_ context.Context, _, _ string) (string, error) {
	if f.idx >= len(f.plans) {
		return "", errors.New("no more scripted plans")
	}
	out := f.plans[f.idx]
	f.idx++
	return out, nil
}

func TestSafeVerb(t *testing.T) {
	good := []string{
		"SELECT 1",
		"  select * from t",
		"-- comment\nSELECT 1",
		"/* hint */ SELECT 1",
		"WITH cte AS (SELECT 1) SELECT * FROM cte",
	}
	for _, s := range good {
		assert.Truef(t, SafeVerb(s), "expected safe: %q", s)
	}
	bad := []string{
		"INSERT INTO t VALUES (1)",
		"UPDATE t SET x=1",
		"DELETE FROM t",
		"REPLACE INTO t VALUES (1)",
		"ALTER TABLE t ADD COLUMN x INT",
		"CALL myproc()",
		"",
	}
	for _, s := range bad {
		assert.Falsef(t, SafeVerb(s), "expected unsafe: %q", s)
	}
}

func TestAnalyzePlan_FullScanFlag(t *testing.T) {
	plan := `{
		"query_block": {
			"select_id": 1,
			"cost_info": {"query_cost": "12.34"},
			"table": {
				"table_name": "users",
				"access_type": "ALL",
				"rows_examined_per_scan": 50000,
				"rows_produced_per_join": 100,
				"attached_condition": "user_id = 5"
			}
		}
	}`
	flags, text, err := AnalyzePlan(plan)
	require.NoError(t, err)
	assert.Contains(t, text, "users")
	kinds := flagKinds(flags)
	assert.Contains(t, kinds, FlagFullScan)
	assert.Contains(t, kinds, FlagBigScan)
}

func TestAnalyzePlan_FilesortAndTemp(t *testing.T) {
	plan := `{
		"query_block": {
			"select_id": 1,
			"using_filesort": true,
			"using_temporary_table": true,
			"table": {"table_name": "t", "access_type": "ref", "rows_examined_per_scan": 5}
		}
	}`
	flags, _, err := AnalyzePlan(plan)
	require.NoError(t, err)
	kinds := flagKinds(flags)
	assert.Contains(t, kinds, FlagFilesort)
	assert.Contains(t, kinds, FlagTempTable)
}

func TestAnalyzePlan_TinyTableNoFullScanFlag(t *testing.T) {
	plan := `{
		"query_block": {
			"table": {"table_name": "t", "access_type": "ALL", "rows_examined_per_scan": 5}
		}
	}`
	flags, _, err := AnalyzePlan(plan)
	require.NoError(t, err)
	for _, f := range flags {
		assert.NotEqualf(t, FlagFullScan, f.Kind, "should not flag full scan on a 5-row table")
	}
}

func TestPlanHash_StableAcrossCostAndRowEstimateChanges(t *testing.T) {
	a := `{"query_block":{"select_id":1,"cost_info":{"query_cost":"1.0"},"table":{"table_name":"t","access_type":"ref","key":"idx","rows_examined_per_scan":1}}}`
	b := `{"query_block":{"select_id":1,"cost_info":{"query_cost":"99.0"},"table":{"table_name":"t","access_type":"ref","key":"idx","rows_examined_per_scan":900000}}}`
	ha, err := PlanHash(a)
	require.NoError(t, err)
	hb, err := PlanHash(b)
	require.NoError(t, err)
	assert.Equal(t, ha, hb, "plan hash must ignore cost and row-estimate fields")
}

func TestPlanHash_DiffersOnAccessType(t *testing.T) {
	a := `{"query_block":{"table":{"table_name":"t","access_type":"ref","key":"idx"}}}`
	b := `{"query_block":{"table":{"table_name":"t","access_type":"ALL"}}}`
	ha, _ := PlanHash(a)
	hb, _ := PlanHash(b)
	assert.NotEqual(t, ha, hb)
}

func TestEngine_RunHappyPath(t *testing.T) {
	plan := `{"query_block":{"table":{"table_name":"t","access_type":"ref","key":"idx","rows_examined_per_scan":10}}}`
	src := &fakeSource{
		examples: map[string]db.Example{"d1": {SQLText: "SELECT * FROM t WHERE id = 1", Schema: "app"}},
		plans:    []string{plan},
	}
	e := New(src)

	res, err := e.Run(context.Background(), "d1")
	require.NoError(t, err)
	assert.False(t, res.Skipped)
	assert.NotEmpty(t, res.PlanHash)
	assert.NotEmpty(t, res.PlanText)
	assert.False(t, res.FromCache)
	assert.False(t, res.Flipped)
}

func TestEngine_RunCachesIdenticalPlans(t *testing.T) {
	plan := `{"query_block":{"table":{"table_name":"t","access_type":"ref","key":"idx"}}}`
	src := &fakeSource{
		examples: map[string]db.Example{"d1": {SQLText: "SELECT 1"}},
		plans:    []string{plan, plan}, // returned on consecutive calls
	}
	e := New(src)

	_, err := e.Run(context.Background(), "d1")
	require.NoError(t, err)
	res2, err := e.Run(context.Background(), "d1")
	require.NoError(t, err)
	assert.True(t, res2.FromCache, "second run with identical plan must be cached")
	assert.False(t, res2.Flipped)
}

func TestEngine_RunDetectsPlanFlip(t *testing.T) {
	planA := `{"query_block":{"table":{"table_name":"t","access_type":"ref","key":"idx"}}}`
	planB := `{"query_block":{"table":{"table_name":"t","access_type":"ALL"}}}`
	src := &fakeSource{
		examples: map[string]db.Example{"d1": {SQLText: "SELECT 1"}},
		plans:    []string{planA, planB},
	}
	e := New(src)

	_, err := e.Run(context.Background(), "d1")
	require.NoError(t, err)
	res2, err := e.Run(context.Background(), "d1")
	require.NoError(t, err)
	assert.True(t, res2.Flipped, "differing hashes must register as a flip")
	assert.NotEmpty(t, res2.PriorHash)
	assert.NotEqual(t, res2.PriorHash, res2.PlanHash)

	flips := e.Flips()
	require.Len(t, flips, 1)
	assert.Equal(t, "d1", flips[0].Digest)
}

func TestEngine_SkipsNonSelect(t *testing.T) {
	src := &fakeSource{
		examples: map[string]db.Example{"d1": {SQLText: "UPDATE t SET x=1"}},
	}
	e := New(src)

	res, err := e.Run(context.Background(), "d1")
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Contains(t, res.SkipReason, "UPDATE")
	assert.Empty(t, res.PlanHash)
}

func TestEngine_SkipsWhenNoExample(t *testing.T) {
	src := &fakeSource{examples: map[string]db.Example{}}
	e := New(src)

	res, err := e.Run(context.Background(), "d1")
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Contains(t, res.SkipReason, "events_statements_history_long")
}

func flagKinds(flags []RedFlag) []string {
	out := make([]string, len(flags))
	for i, f := range flags {
		out[i] = f.Kind
	}
	return out
}

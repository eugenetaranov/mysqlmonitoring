package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eugenetaranov/mysqlmonitoring/internal/insights"
	"github.com/eugenetaranov/mysqlmonitoring/internal/series"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatTop_TextHeaderAndRows(t *testing.T) {
	var buf bytes.Buffer
	FormatTop(&buf, []insights.DigestSummary{{
		Schema: "app", Digest: "abc", Text: "SELECT * FROM users WHERE id=?",
		Calls: 5, AAS: 0.42, CallsPerSec: 0.5,
		AvgLatency: 250 * time.Millisecond, RowsExamined: 1000,
	}})
	out := buf.String()
	assert.Contains(t, out, "AAS")
	assert.Contains(t, out, "Calls/s")
	assert.Contains(t, out, "0.42")
	assert.Contains(t, out, "250ms")
	assert.Contains(t, out, "SELECT * FROM users")
}

func TestFormatTopJSON_NDJSON(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, FormatTopJSON(&buf, []insights.DigestSummary{
		{Schema: "app", Digest: "a", Text: "X", AAS: 1.5, Calls: 1, AvgLatency: time.Second},
		{Schema: "app", Digest: "b", Text: "Y", AAS: 0.5, Calls: 2, AvgLatency: 500 * time.Millisecond},
	}))
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	for _, l := range lines {
		var obj map[string]any
		require.NoError(t, json.Unmarshal([]byte(l), &obj))
		assert.Contains(t, obj, "digest")
		assert.Contains(t, obj, "aas")
		assert.Contains(t, obj, "avg_latency_ms")
	}
}

func TestFormatLoad_TextIncludesTotal(t *testing.T) {
	var buf bytes.Buffer
	FormatLoad(&buf, insights.LoadBreakdown{
		Window: time.Minute,
		Classes: []insights.ClassLoad{
			{Class: series.WaitClassCPU, AAS: 1.5},
			{Class: series.WaitClassIO, AAS: 0.5},
		},
		Total: 2.0,
	})
	out := buf.String()
	assert.Contains(t, out, "CPU")
	assert.Contains(t, out, "IO")
	assert.Contains(t, out, "total")
	assert.Contains(t, out, "2.000")
}

func TestFormatLoadJSON_StructureStable(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, FormatLoadJSON(&buf, insights.LoadBreakdown{
		Window: 30 * time.Second,
		Classes: []insights.ClassLoad{
			{Class: series.WaitClassCPU, AAS: 0.7},
			{Class: series.WaitClassIO, AAS: 0.3},
		},
		Total: 1.0,
	}))
	var obj map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &obj))
	assert.Equal(t, float64(30), obj["window_seconds"])
	assert.Equal(t, float64(1.0), obj["total"])
	classes := obj["classes"].(map[string]any)
	assert.Equal(t, 0.7, classes["CPU"])
	assert.Equal(t, 0.3, classes["IO"])
}

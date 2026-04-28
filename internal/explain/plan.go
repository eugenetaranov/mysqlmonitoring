package explain

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// RedFlag is one optimizer concern surfaced by the renderer. Kind is a
// stable token used by tests and TUI styling; Detail is a short
// human-readable explanation tied to the specific plan node.
type RedFlag struct {
	NodePath string
	Kind     string
	Detail   string
}

// Stable RedFlag.Kind tokens.
const (
	FlagFullScan  = "FULL_SCAN"
	FlagFilesort  = "FILESORT"
	FlagTempTable = "TEMP_TABLE"
	FlagBigScan   = "BIG_SCAN_RATIO"
)

// fullScanRowThreshold is the minimum rows_examined_per_scan that
// turns "type=ALL" from a curiosity into a flag. Tiny tables are
// fine to full-scan and would be noisy if reported.
const fullScanRowThreshold = 1000

// scanProducedRatioThreshold flags nodes that examine far more rows
// than they produce — usually indicates a missing index.
const scanProducedRatioThreshold = 100

// AnalyzePlan parses the JSON returned by EXPLAIN FORMAT=JSON and
// returns a list of RedFlag plus a textual rendering of the plan
// tree. The rendering is intentionally compact — the TUI plan view
// can format more richly on top of the parsed nodes if desired.
func AnalyzePlan(planJSON string) ([]RedFlag, string, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(planJSON), &raw); err != nil {
		return nil, "", fmt.Errorf("parse plan json: %w", err)
	}

	var flags []RedFlag
	var lines []string

	// MySQL 8.0 plans live under "query_block". 5.7 also uses this
	// key. Older formats may not — we handle that gracefully.
	if qb, ok := raw["query_block"].(map[string]any); ok {
		walkNode("query_block", qb, 0, &flags, &lines)
	} else {
		walkNode("(root)", raw, 0, &flags, &lines)
	}

	return flags, strings.Join(lines, "\n"), nil
}

// walkNode recursively walks a JSON object that represents either a
// query_block or a table node, accumulates lines and flags as it
// descends, and recurses into any nested sub-blocks.
func walkNode(label string, node map[string]any, depth int, flags *[]RedFlag, lines *[]string) {
	indent := strings.Repeat("  ", depth)

	if t, ok := node["table"].(map[string]any); ok {
		describeTable(label+".table", t, depth, flags, lines)
		// recurse into nested tables under the same query_block
	}
	if cost, ok := node["cost_info"].(map[string]any); ok {
		if total := cost["query_cost"]; total != nil {
			*lines = append(*lines, fmt.Sprintf("%s%s cost=%v", indent, label, total))
		}
	}

	// Top-level concerns reported on query_block itself.
	if b, ok := boolField(node, "using_filesort"); ok && b {
		*flags = append(*flags, RedFlag{NodePath: label, Kind: FlagFilesort, Detail: "query_block uses filesort"})
	}
	if b, ok := boolField(node, "using_temporary_table"); ok && b {
		*flags = append(*flags, RedFlag{NodePath: label, Kind: FlagTempTable, Detail: "query_block creates a temporary table"})
	}

	// Recurse into ordering/grouping/nested blocks.
	for _, k := range []string{"ordering_operation", "grouping_operation", "duplicates_removal", "windowing"} {
		if sub, ok := node[k].(map[string]any); ok {
			walkNode(label+"."+k, sub, depth+1, flags, lines)
		}
	}
	if arr, ok := node["nested_loop"].([]any); ok {
		for i, n := range arr {
			if m, ok := n.(map[string]any); ok {
				walkNode(fmt.Sprintf("%s.nested_loop[%d]", label, i), m, depth+1, flags, lines)
			}
		}
	}
	if arr, ok := node["query_specifications"].([]any); ok {
		for i, n := range arr {
			if m, ok := n.(map[string]any); ok {
				walkNode(fmt.Sprintf("%s.query_specifications[%d]", label, i), m, depth+1, flags, lines)
			}
		}
	}
}

// describeTable emits one rendering line for a "table" node and
// records any red flags attached to it.
func describeTable(path string, t map[string]any, depth int, flags *[]RedFlag, lines *[]string) {
	indent := strings.Repeat("  ", depth+1)
	name, _ := t["table_name"].(string)
	access, _ := t["access_type"].(string)
	key, _ := t["key"].(string)
	rowsScan := numField(t, "rows_examined_per_scan")
	rowsProd := numField(t, "rows_produced_per_join")
	condition, _ := t["attached_condition"].(string)

	parts := []string{name}
	if access != "" {
		parts = append(parts, "access="+access)
	}
	if key != "" {
		parts = append(parts, "key="+key)
	}
	if rowsScan > 0 {
		parts = append(parts, fmt.Sprintf("rows_examined=%d", rowsScan))
	}
	*lines = append(*lines, indent+"table "+strings.Join(parts, " "))
	if condition != "" {
		*lines = append(*lines, indent+"  cond: "+condition)
	}

	if access == "ALL" && rowsScan >= fullScanRowThreshold {
		*flags = append(*flags, RedFlag{
			NodePath: path,
			Kind:     FlagFullScan,
			Detail:   fmt.Sprintf("%s: full scan over %d rows (no index used)", name, rowsScan),
		})
	}
	if rowsProd > 0 && rowsScan >= rowsProd*scanProducedRatioThreshold {
		*flags = append(*flags, RedFlag{
			NodePath: path,
			Kind:     FlagBigScan,
			Detail: fmt.Sprintf("%s: examines %d rows to produce %d (ratio %dx > %dx threshold)",
				name, rowsScan, rowsProd, rowsScan/maxInt(1, rowsProd), scanProducedRatioThreshold),
		})
	}
	if b, ok := boolField(t, "using_filesort"); ok && b {
		*flags = append(*flags, RedFlag{NodePath: path, Kind: FlagFilesort, Detail: name + ": filesort on this table"})
	}
	if b, ok := boolField(t, "using_temporary_table"); ok && b {
		*flags = append(*flags, RedFlag{NodePath: path, Kind: FlagTempTable, Detail: name + ": temporary table on this node"})
	}
}

func numField(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

func boolField(m map[string]any, key string) (bool, bool) {
	v, ok := m[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func maxInt(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

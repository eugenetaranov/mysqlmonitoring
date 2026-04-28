package explain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// canonicalKeys are the structural fields whose values define the
// shape of a plan. Cost numbers, exact row estimates and timing
// fields are excluded so cardinality drift does not produce a
// spurious plan flip.
var canonicalKeys = map[string]struct{}{
	"select_id":           {},
	"table_name":          {},
	"access_type":         {},
	"key":                 {},
	"key_length":          {},
	"ref":                 {},
	"used_key_parts":      {},
	"used_columns":        {},
	"attached_condition":  {},
	"using_index":         {},
	"using_index_for_group_by": {},
	"using_filesort":      {},
	"using_temporary_table": {},
	"index_condition":     {},
	"rows_produced_per_join_is_estimated": {},
}

// nestedKeys names children of a query_block or table node that we
// recurse into when canonicalising. We intentionally do not recurse
// into "cost_info" or any timing data.
var nestedKeys = []string{
	"table",
	"nested_loop",
	"query_specifications",
	"ordering_operation",
	"grouping_operation",
	"duplicates_removal",
	"windowing",
}

// PlanHash returns a stable hash of a plan's structural skeleton.
// Two plans hash to the same value iff every canonicalKeys-valued
// field at every node matches and the tree shape is identical.
func PlanHash(planJSON string) (string, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(planJSON), &raw); err != nil {
		return "", fmt.Errorf("parse plan json: %w", err)
	}

	var sb strings.Builder
	root := raw
	if qb, ok := raw["query_block"].(map[string]any); ok {
		root = qb
	}
	canonicalise(&sb, root)

	h := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(h[:]), nil
}

// canonicalise serialises node into sb using a deterministic format:
// keys appear in alphabetical order, only canonicalKeys leaf fields
// are included, and nested children are recursed into in nestedKeys
// order.
func canonicalise(sb *strings.Builder, node map[string]any) {
	keys := make([]string, 0, len(node))
	for k := range node {
		if _, ok := canonicalKeys[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		writeAny(sb, node[k])
	}
	for _, k := range nestedKeys {
		v, ok := node[k]
		if !ok {
			continue
		}
		sb.WriteByte('|')
		sb.WriteString(k)
		sb.WriteByte(':')
		switch m := v.(type) {
		case map[string]any:
			canonicalise(sb, m)
		case []any:
			sb.WriteByte('[')
			for i, item := range m {
				if i > 0 {
					sb.WriteByte(',')
				}
				if mm, ok := item.(map[string]any); ok {
					canonicalise(sb, mm)
				} else {
					writeAny(sb, item)
				}
			}
			sb.WriteByte(']')
		default:
			writeAny(sb, v)
		}
	}
	sb.WriteByte('}')
}

func writeAny(sb *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		sb.WriteString("null")
	case string:
		sb.WriteByte('"')
		sb.WriteString(x)
		sb.WriteByte('"')
	case bool:
		if x {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case []any:
		sb.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeAny(sb, item)
		}
		sb.WriteByte(']')
	default:
		fmt.Fprintf(sb, "%v", x)
	}
}

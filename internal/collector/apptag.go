package collector

import (
	"net/url"
	"strings"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
)

// commentMaxBytes caps the leading SQL comment length we are willing
// to parse. Anything longer is ignored entirely and the resolver falls
// through to program_name.
const commentMaxBytes = 1024

// AppTagUnknown is the literal returned when no source yields a tag.
const AppTagUnknown = "unknown"

// ParseSQLComment extracts key/value pairs from a single leading
// sqlcommenter-style block comment. Values may be single-quoted and
// URL-encoded; both forms are tolerated. Malformed pairs are skipped
// without raising an error. The returned map is empty (never nil)
// when no recognisable pairs are found.
//
// The parser only considers a comment if it begins at byte 0 of sql,
// after stripping leading ASCII whitespace. Trailing content after
// the closing "*/" is irrelevant — we only look at the comment body.
func ParseSQLComment(sql string) map[string]string {
	out := make(map[string]string)

	s := strings.TrimLeft(sql, " \t\r\n")
	if !strings.HasPrefix(s, "/*") {
		return out
	}
	end := strings.Index(s, "*/")
	if end < 0 {
		return out
	}
	body := s[2:end]
	if len(body) > commentMaxBytes {
		return out
	}

	for _, pair := range splitPairs(body) {
		key, val, ok := splitKV(pair)
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key == "" {
			continue
		}
		val = unquote(val)
		if decoded, err := url.QueryUnescape(val); err == nil {
			val = decoded
		}
		if val == "" {
			continue
		}
		out[key] = val
	}
	return out
}

// splitPairs splits the comment body on commas that are not inside a
// single-quoted string. A naive strings.Split would mangle values
// like service='foo,bar'.
func splitPairs(body string) []string {
	var pairs []string
	var (
		start    int
		inQuotes bool
	)
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '\'' && (i == 0 || body[i-1] != '\\'):
			inQuotes = !inQuotes
		case c == ',' && !inQuotes:
			pairs = append(pairs, body[start:i])
			start = i + 1
		}
	}
	pairs = append(pairs, body[start:])
	return pairs
}

// splitKV splits one pair on the first '=' outside of quotes.
func splitKV(pair string) (string, string, bool) {
	for i := 0; i < len(pair); i++ {
		if pair[i] == '=' {
			return pair[:i], pair[i+1:], true
		}
	}
	return "", "", false
}

// unquote strips matched single quotes from val. Embedded escapes
// like \\' are converted back to '. Unbalanced quoting yields the
// original string unchanged so the parser remains tolerant.
func unquote(val string) string {
	if len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'' {
		inner := val[1 : len(val)-1]
		return strings.ReplaceAll(inner, `\'`, `'`)
	}
	return val
}

// ResolveAppTag picks the best available application tag for stmt
// using, in order: a "service" key from the leading SQL comment, the
// program_name connect attribute, then AppTagUnknown.
func ResolveAppTag(stmt db.CurrentStmt) string {
	if stmt.SQLText != "" {
		if pairs := ParseSQLComment(stmt.SQLText); len(pairs) > 0 {
			if v, ok := pairs["service"]; ok && v != "" {
				return v
			}
		}
	}
	if stmt.ProgramName != "" {
		return stmt.ProgramName
	}
	return AppTagUnknown
}

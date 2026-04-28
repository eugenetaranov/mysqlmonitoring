package explain

import (
	"strings"
	"unicode"
)

// SafeVerb reports whether sql begins with a verb we are willing to
// run EXPLAIN against. Only SELECT is considered safe, because EXPLAIN
// on DML can have unintended side effects depending on server version
// and EXPLAIN flavour. Leading whitespace and SQL comments are
// skipped.
func SafeVerb(sql string) bool {
	s := stripLeading(sql)
	verb := firstWord(s)
	return strings.EqualFold(verb, "SELECT") || strings.EqualFold(verb, "WITH")
}

// VerbFor returns the first verb extracted from sql, uppercased, for
// inclusion in error messages.
func VerbFor(sql string) string {
	s := stripLeading(sql)
	v := firstWord(s)
	return strings.ToUpper(v)
}

// stripLeading trims whitespace and any number of leading SQL
// comments (block /* ... */ and line --). It returns the remaining
// statement starting at the first verb.
func stripLeading(sql string) string {
	for {
		s := strings.TrimLeftFunc(sql, unicode.IsSpace)
		switch {
		case strings.HasPrefix(s, "/*"):
			end := strings.Index(s, "*/")
			if end < 0 {
				return ""
			}
			sql = s[end+2:]
		case strings.HasPrefix(s, "--"):
			nl := strings.IndexByte(s, '\n')
			if nl < 0 {
				return ""
			}
			sql = s[nl+1:]
		case strings.HasPrefix(s, "#"):
			nl := strings.IndexByte(s, '\n')
			if nl < 0 {
				return ""
			}
			sql = s[nl+1:]
		default:
			return s
		}
	}
}

// firstWord returns the leading word of s, terminated by whitespace
// or punctuation.
func firstWord(s string) string {
	for i, r := range s {
		if !unicode.IsLetter(r) {
			return s[:i]
		}
	}
	return s
}

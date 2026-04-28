package collector

import (
	"strings"
	"testing"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/stretchr/testify/assert"
)

func TestParseSQLComment_HappyPath(t *testing.T) {
	got := ParseSQLComment(`/* service='checkout',route='POST /cart' */ SELECT 1`)
	assert.Equal(t, "checkout", got["service"])
	assert.Equal(t, "POST /cart", got["route"])
}

func TestParseSQLComment_URLEncodedValues(t *testing.T) {
	got := ParseSQLComment(`/* service='checkout',trace_id='abc%20123' */ SELECT 1`)
	assert.Equal(t, "abc 123", got["trace_id"])
}

func TestParseSQLComment_UnquotedValues(t *testing.T) {
	got := ParseSQLComment(`/* service=checkout, env=prod */SELECT 1`)
	assert.Equal(t, "checkout", got["service"])
	assert.Equal(t, "prod", got["env"])
}

func TestParseSQLComment_NoLeadingComment(t *testing.T) {
	got := ParseSQLComment(`SELECT 1 /* service='x' */`)
	assert.Empty(t, got, "comment must be at the start of the statement")
}

func TestParseSQLComment_OversizedRejected(t *testing.T) {
	body := strings.Repeat("k=v,", 300) // body length ~1200 > cap
	got := ParseSQLComment("/* " + body + " */ SELECT 1")
	assert.Empty(t, got)
}

func TestParseSQLComment_MalformedPairsTolerated(t *testing.T) {
	got := ParseSQLComment(`/* service=, =orphan, env='prod', broken */ SELECT 1`)
	assert.Equal(t, "prod", got["env"])
	assert.NotContains(t, got, "service")
	assert.NotContains(t, got, "")
}

func TestParseSQLComment_CommaInsideQuotesIsKept(t *testing.T) {
	got := ParseSQLComment(`/* service='a,b', other='c' */ SELECT 1`)
	assert.Equal(t, "a,b", got["service"])
	assert.Equal(t, "c", got["other"])
}

func TestParseSQLComment_UnclosedComment(t *testing.T) {
	got := ParseSQLComment(`/* service='x' SELECT 1`)
	assert.Empty(t, got)
}

func TestResolveAppTag_CommentWins(t *testing.T) {
	stmt := db.CurrentStmt{
		SQLText:     `/* service='checkout' */ SELECT 1`,
		ProgramName: "orders-api",
	}
	assert.Equal(t, "checkout", ResolveAppTag(stmt))
}

func TestResolveAppTag_FallsBackToProgramName(t *testing.T) {
	stmt := db.CurrentStmt{
		SQLText:     "SELECT 1",
		ProgramName: "orders-api",
	}
	assert.Equal(t, "orders-api", ResolveAppTag(stmt))
}

func TestResolveAppTag_FallsBackToUnknown(t *testing.T) {
	stmt := db.CurrentStmt{SQLText: "SELECT 1"}
	assert.Equal(t, AppTagUnknown, ResolveAppTag(stmt))
}

func TestResolveAppTag_EmptyServiceFallsThrough(t *testing.T) {
	stmt := db.CurrentStmt{
		SQLText:     `/* service='', route='x' */ SELECT 1`,
		ProgramName: "orders-api",
	}
	assert.Equal(t, "orders-api", ResolveAppTag(stmt))
}

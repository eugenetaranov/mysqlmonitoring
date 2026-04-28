package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHelpSectionsFor_IssuesIncludesViewSpecificAndGlobal(t *testing.T) {
	got := helpSectionsFor(ViewIssues, true)
	require := []string{"Issues", "Global"}
	gotTitles := make([]string, len(got))
	for i, s := range got {
		gotTitles[i] = s.title
	}
	for _, want := range require {
		assert.Contains(t, gotTitles, want)
	}
}

func TestHelpSectionsFor_LockSkipsIssuesEntries(t *testing.T) {
	got := helpSectionsFor(ViewLock, false)
	for _, sec := range got {
		if sec.title == "Issues" {
			t.Fatalf("did not expect Issues section while in Lock view: %+v", got)
		}
	}
}

func TestRenderHelp_IncludesQuestionMarkAndDismissHint(t *testing.T) {
	m := Model{view: ViewHelp, helpReturn: ViewIssues, insights: nil}
	out := renderHelp(m)
	assert.Contains(t, out, "Keyboard shortcuts")
	assert.Contains(t, out, "press any key to dismiss")
	assert.Contains(t, out, "?")
	assert.Contains(t, out, "y")
	assert.Contains(t, out, "K")
	assert.Contains(t, out, "tab")
}

func TestRenderHelp_DifferentSectionsByView(t *testing.T) {
	issues := renderHelp(Model{view: ViewHelp, helpReturn: ViewIssues})
	lock := renderHelp(Model{view: ViewHelp, helpReturn: ViewLock})
	top := renderHelp(Model{view: ViewHelp, helpReturn: ViewTop})

	assert.True(t, strings.Contains(issues, "open issue detail"))
	assert.True(t, strings.Contains(lock, "next blocker"))
	assert.True(t, strings.Contains(top, "EXPLAIN"))
}

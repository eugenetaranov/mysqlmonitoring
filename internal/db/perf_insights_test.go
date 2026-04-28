package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyDigestWarning_GlobalInstrumentationOff(t *testing.T) {
	got := classifyDigestWarning(map[string]bool{
		"global_instrumentation": false,
		"statements_digest":      true,
	})
	assert.Contains(t, got, "global_instrumentation")
	assert.Contains(t, got, "ENABLED='YES'")
}

func TestClassifyDigestWarning_StatementsDigestOff(t *testing.T) {
	got := classifyDigestWarning(map[string]bool{
		"global_instrumentation": true,
		"statements_digest":      false,
	})
	assert.Contains(t, got, "statements_digest")
}

func TestClassifyDigestWarning_BothOff(t *testing.T) {
	got := classifyDigestWarning(map[string]bool{
		"global_instrumentation": false,
		"statements_digest":      false,
	})
	// global_instrumentation is named first in the cascade because
	// turning it on is the prerequisite — so that hint wins.
	assert.Contains(t, got, "global_instrumentation")
}

func TestClassifyDigestWarning_FallthroughOnEmptyMap(t *testing.T) {
	got := classifyDigestWarning(map[string]bool{})
	assert.NotEmpty(t, got)
}

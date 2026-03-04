package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQueryLabel(t *testing.T) {
	tests := []struct {
		name     string
		digest   string
		rawQuery string
		want     string
	}{
		{
			name:     "digest present",
			digest:   "UPDATE `accounts` SET `balance` = `balance` + ? WHERE `id` = ?",
			rawQuery: "UPDATE accounts SET balance = balance + 100 WHERE id = 42",
			want:     "UPDATE `accounts` SET `balance` = `balance` + ? WHERE `id` = ?",
		},
		{
			name:     "digest empty falls back to simplifyQuery",
			digest:   "",
			rawQuery: "UPDATE accounts SET balance = balance + 100 WHERE id = 42",
			want:     "UPDATE accounts",
		},
		{
			name:     "both empty",
			digest:   "",
			rawQuery: "",
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := queryLabel(tt.digest, tt.rawQuery)
			assert.Equal(t, tt.want, got)
		})
	}
}

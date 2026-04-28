package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDSN(t *testing.T) {
	tests := []struct {
		name string
		cfg  DSNConfig
		want string
	}{
		{
			name: "full config",
			cfg:  DSNConfig{Host: "localhost", Port: 3306, User: "root", Password: "secret", Database: "testdb"},
			want: "root:secret@tcp(localhost:3306)/testdb?parseTime=true&timeout=5s&readTimeout=60s&interpolateParams=true",
		},
		{
			name: "defaults",
			cfg:  DSNConfig{Host: "db.example.com"},
			want: "root:@tcp(db.example.com:3306)/?parseTime=true&timeout=5s&readTimeout=60s&interpolateParams=true",
		},
		{
			name: "custom port",
			cfg:  DSNConfig{Host: "localhost", Port: 3307, User: "admin", Password: "pass", Database: "mydb"},
			want: "admin:pass@tcp(localhost:3307)/mydb?parseTime=true&timeout=5s&readTimeout=60s&interpolateParams=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildDSN(tt.cfg)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseDSN(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		want    string
		wantErr bool
	}{
		{
			name: "go-sql-driver format with parseTime",
			dsn:  "root:pass@tcp(localhost:3306)/db?parseTime=true",
			want: "root:pass@tcp(localhost:3306)/db?parseTime=true",
		},
		{
			name: "go-sql-driver format without parseTime",
			dsn:  "root:pass@tcp(localhost:3306)/db",
			want: "root:pass@tcp(localhost:3306)/db?parseTime=true",
		},
		{
			name: "go-sql-driver format with other params",
			dsn:  "root:pass@tcp(localhost:3306)/db?timeout=5s",
			want: "root:pass@tcp(localhost:3306)/db?timeout=5s&parseTime=true",
		},
		{
			name: "mysql URI format",
			dsn:  "mysql://root:pass@localhost:3306/db",
			want: "root:pass@tcp(localhost:3306)/db?parseTime=true",
		},
		{
			name: "mysql URI format default port",
			dsn:  "mysql://root:pass@localhost/db",
			want: "root:pass@tcp(localhost:3306)/db?parseTime=true",
		},
		{
			name: "simplified format with port",
			dsn:  "root:pass@localhost:3306/db",
			want: "root:pass@tcp(localhost:3306)/db?parseTime=true",
		},
		{
			name: "simplified format without port",
			dsn:  "root:pass@localhost/db",
			want: "root:pass@tcp(localhost)/db?parseTime=true",
		},
		{
			name: "simplified format with params",
			dsn:  "root:pass@localhost:3306/db?timeout=5s",
			want: "root:pass@tcp(localhost:3306)/db?timeout=5s&parseTime=true",
		},
		{
			name: "simplified format no database",
			dsn:  "root:pass@localhost:3306",
			want: "root:pass@tcp(localhost:3306)?parseTime=true",
		},
		{
			name:    "empty DSN",
			dsn:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDSN(tt.dsn)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

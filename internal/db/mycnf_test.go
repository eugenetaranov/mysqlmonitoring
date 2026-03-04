package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadMyCnf_ClientSection(t *testing.T) {
	content := `
[client]
user = dbuser
password = dbpass
host = dbhost
port = 3307
database = mydb
`
	path := writeTempFile(t, content)

	cfg, err := ReadMyCnf(path)
	require.NoError(t, err)
	assert.Equal(t, "dbuser", cfg.User)
	assert.Equal(t, "dbpass", cfg.Password)
	assert.Equal(t, "dbhost", cfg.Host)
	assert.Equal(t, 3307, cfg.Port)
	assert.Equal(t, "mydb", cfg.Database)
}

func TestReadMyCnf_MissingFile(t *testing.T) {
	cfg, err := ReadMyCnf("/nonexistent/.my.cnf")
	require.NoError(t, err)
	assert.Equal(t, DSNConfig{}, cfg)
}

func TestReadMyCnf_NoClientSection(t *testing.T) {
	content := `
[mysqld]
user = mysql
port = 3306
`
	path := writeTempFile(t, content)

	cfg, err := ReadMyCnf(path)
	require.NoError(t, err)
	assert.Equal(t, DSNConfig{}, cfg)
}

func TestReadMyCnf_QuotedValues(t *testing.T) {
	content := `
[client]
user = "quoted_user"
password = 'quoted_pass'
host = bare_host
`
	path := writeTempFile(t, content)

	cfg, err := ReadMyCnf(path)
	require.NoError(t, err)
	assert.Equal(t, "quoted_user", cfg.User)
	assert.Equal(t, "quoted_pass", cfg.Password)
	assert.Equal(t, "bare_host", cfg.Host)
}

func TestReadMyCnf_NoSpacesAroundEquals(t *testing.T) {
	content := `
[client]
user=compact_user
password=compact_pass
`
	path := writeTempFile(t, content)

	cfg, err := ReadMyCnf(path)
	require.NoError(t, err)
	assert.Equal(t, "compact_user", cfg.User)
	assert.Equal(t, "compact_pass", cfg.Password)
}

func TestReadMyCnf_CommentsIgnored(t *testing.T) {
	content := `
# This is a comment
; This is also a comment
[client]
user = testuser
# password = secret
`
	path := writeTempFile(t, content)

	cfg, err := ReadMyCnf(path)
	require.NoError(t, err)
	assert.Equal(t, "testuser", cfg.User)
	assert.Equal(t, "", cfg.Password)
}

func TestReadMyCnf_MultipleSections(t *testing.T) {
	content := `
[mysqld]
port = 9999

[client]
user = clientuser
port = 3307

[mysqldump]
user = dumpuser
`
	path := writeTempFile(t, content)

	cfg, err := ReadMyCnf(path)
	require.NoError(t, err)
	assert.Equal(t, "clientuser", cfg.User)
	assert.Equal(t, 3307, cfg.Port)
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".my.cnf")
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	return path
}

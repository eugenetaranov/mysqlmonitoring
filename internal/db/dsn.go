package db

import (
	"fmt"
	"net/url"
	"strings"
)

// DSNConfig holds the components for building a MySQL DSN.
type DSNConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

// BuildDSN constructs a go-sql-driver/mysql DSN string from config.
func BuildDSN(cfg DSNConfig) string {
	if cfg.Port == 0 {
		cfg.Port = 3306
	}
	if cfg.User == "" {
		cfg.User = "root"
	}

	// readTimeout is intentionally well above the per-query context timeout so it
	// acts as a backstop for dead sockets without preempting context cancellation
	// (which is what triggers the driver's KILL QUERY path).
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&timeout=5s&readTimeout=60s&interpolateParams=true",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)
	return dsn
}

// ParseDSN validates and normalizes a DSN string.
// Accepts go-sql-driver format, mysql:// URI format, or simplified user:pass@host:port/db format.
func ParseDSN(dsn string) (string, error) {
	if dsn == "" {
		return "", fmt.Errorf("DSN is empty")
	}

	// Handle mysql:// URI format
	if strings.HasPrefix(dsn, "mysql://") {
		return convertURIToDSN(dsn)
	}

	// Handle simplified format: user:pass@host:port/db -> wrap host:port in tcp()
	if strings.Contains(dsn, "@") && !strings.Contains(dsn, "@tcp(") {
		dsn = convertSimpleDSN(dsn)
	}

	// Add parseTime if missing
	if !strings.Contains(dsn, "parseTime") {
		if strings.Contains(dsn, "?") {
			dsn += "&parseTime=true"
		} else {
			dsn += "?parseTime=true"
		}
	}

	return dsn, nil
}

// convertSimpleDSN converts user:pass@host:port/db to user:pass@tcp(host:port)/db.
func convertSimpleDSN(dsn string) string {
	atIdx := strings.LastIndex(dsn, "@")
	userInfo := dsn[:atIdx]
	rest := dsn[atIdx+1:] // host:port/db?params

	// Split off query params
	hostDB := rest
	params := ""
	if qIdx := strings.Index(rest, "?"); qIdx >= 0 {
		hostDB = rest[:qIdx]
		params = rest[qIdx:]
	}

	// Split host:port from /db
	slashIdx := strings.Index(hostDB, "/")
	hostPort := hostDB
	dbPart := ""
	if slashIdx >= 0 {
		hostPort = hostDB[:slashIdx]
		dbPart = hostDB[slashIdx:]
	}

	return userInfo + "@tcp(" + hostPort + ")" + dbPart + params
}

func convertURIToDSN(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("invalid DSN URI: %w", err)
	}

	user := u.User.Username()
	password, _ := u.User.Password()
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	database := strings.TrimPrefix(u.Path, "/")

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true",
		user, password, host, port, database)

	// Forward query params
	if u.RawQuery != "" {
		for key, values := range u.Query() {
			if key == "parseTime" {
				continue
			}
			for _, v := range values {
				dsn += "&" + key + "=" + v
			}
		}
	}

	return dsn, nil
}

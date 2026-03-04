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

	// user:password@tcp(host:port)/database?params
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&timeout=5s&readTimeout=10s",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)
	return dsn
}

// ParseDSN validates and normalizes a DSN string.
// Accepts either go-sql-driver format or mysql:// URI format.
func ParseDSN(dsn string) (string, error) {
	if dsn == "" {
		return "", fmt.Errorf("DSN is empty")
	}

	// Handle mysql:// URI format
	if strings.HasPrefix(dsn, "mysql://") {
		return convertURIToDSN(dsn)
	}

	// Assume go-sql-driver format, add parseTime if missing
	if !strings.Contains(dsn, "parseTime") {
		if strings.Contains(dsn, "?") {
			dsn += "&parseTime=true"
		} else {
			dsn += "?parseTime=true"
		}
	}

	return dsn, nil
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

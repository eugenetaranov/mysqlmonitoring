package db

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultMyCnfPath returns the default path to ~/.my.cnf.
func DefaultMyCnfPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".my.cnf")
}

// ReadMyCnf parses a .my.cnf file and returns connection config from the [client] section.
// Returns zero-value DSNConfig and nil error if the file doesn't exist.
func ReadMyCnf(path string) (DSNConfig, error) {
	var cfg DSNConfig

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	defer f.Close()

	inClientSection := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}

		// Section header
		if line[0] == '[' {
			inClientSection = strings.EqualFold(strings.TrimSpace(line[1:strings.IndexByte(line, ']')]), "client")
			continue
		}

		if !inClientSection {
			continue
		}

		key, value, ok := parseINILine(line)
		if !ok {
			continue
		}

		switch strings.ToLower(key) {
		case "user":
			cfg.User = value
		case "password":
			cfg.Password = value
		case "host":
			cfg.Host = value
		case "port":
			if p, err := strconv.Atoi(value); err == nil {
				cfg.Port = p
			}
		case "database":
			cfg.Database = value
		}
	}

	return cfg, scanner.Err()
}

// parseINILine parses "key=value" or "key = value", stripping quotes from values.
func parseINILine(line string) (key, value string, ok bool) {
	idx := strings.IndexByte(line, '=')
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])

	// Strip surrounding quotes
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
	}

	return key, value, true
}

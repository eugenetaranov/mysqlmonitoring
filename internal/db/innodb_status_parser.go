package db

import (
	"regexp"
	"strconv"
	"strings"
)

// ParseInnoDBStatus parses the output of SHOW ENGINE INNODB STATUS.
func ParseInnoDBStatus(raw string) InnoDBStatus {
	result := InnoDBStatus{Raw: raw}

	result.LatestDeadlock = parseDeadlockSection(raw)
	result.TransactionCount, result.ActiveTrxCount, result.LockWaitCount = parseTransactionCounts(raw)

	return result
}

// sectionRe matches section headers like:
// ------------------------
// LATEST DETECTED DEADLOCK
// ------------------------
var sectionRe = regexp.MustCompile(`(?m)^-{3,}\n([A-Z][A-Z /]+)\n-{3,}$`)

// extractSection extracts the content between a named section header and the next section header.
func extractSection(raw, name string) string {
	matches := sectionRe.FindAllStringSubmatchIndex(raw, -1)
	for i, match := range matches {
		sectionName := strings.TrimSpace(raw[match[2]:match[3]])
		if sectionName == name {
			start := match[1] // end of the header block
			var end int
			if i+1 < len(matches) {
				end = matches[i+1][0]
			} else {
				end = len(raw)
			}
			return raw[start:end]
		}
	}
	return ""
}

func parseDeadlockSection(raw string) *DeadlockInfo {
	section := extractSection(raw, "LATEST DETECTED DEADLOCK")
	if section == "" {
		return nil
	}

	dl := &DeadlockInfo{}

	// First non-empty line is the timestamp, e.g. "2024-01-15 10:25:30 0x7f1234567890"
	// Extract just the datetime portion (first 19 chars: YYYY-MM-DD HH:MM:SS)
	lines := strings.Split(section, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if len(trimmed) >= 19 {
				dl.Timestamp = trimmed[:19]
			} else {
				dl.Timestamp = trimmed
			}
			break
		}
	}

	dl.Transactions = parseDeadlockTransactions(section)
	return dl
}

var (
	trxHeaderRe = regexp.MustCompile(`\*\*\* \((\d+)\) TRANSACTION:`)
	trxIDRe     = regexp.MustCompile(`TRANSACTION (\d+)`)
	threadIDRe  = regexp.MustCompile(`thread id (\d+)`)
	queryUserRe = regexp.MustCompile(`(?:(?:MySQL )?thread id \d+, OS thread handle \w+, query id \d+ )(\S+)\s+(\S+)`)
	lockModeRe  = regexp.MustCompile(`lock mode (\w+)`)
	lockTableRe = regexp.MustCompile("(?:table `)([^`]+)`\\.`([^`]+)`")
	lockIndexRe = regexp.MustCompile("`?index `?([^`\\s]+)`?")
)

func parseDeadlockTransactions(section string) []DeadlockTransaction {
	var transactions []DeadlockTransaction

	matches := trxHeaderRe.FindAllStringSubmatchIndex(section, -1)
	for i, match := range matches {
		var end int
		if i+1 < len(matches) {
			end = matches[i+1][0]
		} else {
			end = len(section)
		}

		trxSection := section[match[0]:end]
		trx := DeadlockTransaction{}

		if m := trxIDRe.FindStringSubmatch(trxSection); m != nil {
			trx.TrxID = m[1]
		}
		if m := threadIDRe.FindStringSubmatch(trxSection); m != nil {
			id, _ := strconv.ParseUint(m[1], 10, 64)
			trx.ThreadID = id
		}
		if m := queryUserRe.FindStringSubmatch(trxSection); m != nil {
			trx.Host = m[1]
			trx.User = m[2]
		}
		if m := lockModeRe.FindStringSubmatch(trxSection); m != nil {
			trx.LockMode = m[1]
		}
		if m := lockTableRe.FindStringSubmatch(trxSection); m != nil {
			trx.TableName = m[1] + "." + m[2]
		}
		if m := lockIndexRe.FindStringSubmatch(trxSection); m != nil {
			trx.IndexName = m[1]
		}

		// Find query - the line immediately after the "thread id" / "MySQL thread id" line.
		// In InnoDB deadlock output the SQL is always on the next line after the thread info.
		trxLines := strings.Split(trxSection, "\n")
		for j, line := range trxLines {
			trimmed := strings.TrimSpace(line)
			if strings.Contains(trimmed, "thread id") || strings.Contains(trimmed, "MySQL thread id") {
				if j+1 < len(trxLines) {
					ql := strings.TrimSpace(trxLines[j+1])
					if ql != "" && !strings.HasPrefix(ql, "***") {
						trx.Query = ql
					}
				}
				break
			}
		}

		transactions = append(transactions, trx)
	}

	return transactions
}

func parseTransactionCounts(raw string) (total, active, lockWait int) {
	section := extractSection(raw, "TRANSACTIONS")
	if section == "" {
		return 0, 0, 0
	}

	trxCountRe := regexp.MustCompile(`Trx id counter (\d+)`)
	if m := trxCountRe.FindStringSubmatch(section); m != nil {
		total, _ = strconv.Atoi(m[1])
	}

	active = strings.Count(section, "ACTIVE")
	lockWait = strings.Count(section, "LOCK WAIT")

	return total, active, lockWait
}

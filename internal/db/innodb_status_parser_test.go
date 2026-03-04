package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleInnoDBStatus = `
=====================================
2024-01-15 10:30:45 0x7f1234567890 INNODB MONITOR OUTPUT
=====================================
Per second averages calculated from the last 30 seconds
-----------------
BACKGROUND THREAD
-----------------
srv_master_thread loops: 100 srv_active, 0 srv_shutdown, 500 srv_idle
------------------------
LATEST DETECTED DEADLOCK
------------------------
2024-01-15 10:25:30 0x7f1234567890
*** (1) TRANSACTION:
TRANSACTION 12345, ACTIVE 5 sec starting index read
thread id 100, OS thread handle 123456, query id 500 localhost root updating
UPDATE users SET name='test' WHERE id=1
*** (1) WAITING FOR THIS LOCK TO BE GRANTED:
RECORD LOCKS space id 10 page no 3 n bits 72 index PRIMARY of table ` + "`testdb`.`users`" + ` trx id 12345 lock_mode X locks rec but not gap waiting
*** (2) TRANSACTION:
TRANSACTION 12346, ACTIVE 3 sec starting index read
thread id 101, OS thread handle 123457, query id 501 localhost root updating
UPDATE orders SET status='done' WHERE user_id=1
*** (2) WAITING FOR THIS LOCK TO BE GRANTED:
RECORD LOCKS space id 11 page no 5 n bits 80 index PRIMARY of table ` + "`testdb`.`orders`" + ` trx id 12346 lock_mode X locks rec but not gap waiting
*** WE ROLL BACK TRANSACTION (2)
------------
TRANSACTIONS
------------
Trx id counter 12350
Purge done for trx's n:o < 12300 undo n:o < 0 state: running
---TRANSACTION 12349, ACTIVE 10 sec
2 lock struct(s), heap size 1136, 1 row lock(s)
MySQL thread id 102, OS thread handle 0x7f1234, query id 505 localhost root
---TRANSACTION 12348, ACTIVE 2 sec
LOCK WAIT 3 lock struct(s), heap size 1136, 2 row lock(s)
MySQL thread id 103, OS thread handle 0x7f1235, query id 506 localhost root
--------
FILE I/O
--------
`

func TestParseInnoDBStatus(t *testing.T) {
	result := ParseInnoDBStatus(sampleInnoDBStatus)

	assert.NotEmpty(t, result.Raw)
	assert.Equal(t, 2, result.ActiveTrxCount)
	assert.Equal(t, 1, result.LockWaitCount)
}

func TestParseDeadlockSection(t *testing.T) {
	result := ParseInnoDBStatus(sampleInnoDBStatus)

	require.NotNil(t, result.LatestDeadlock)
	assert.Equal(t, "2024-01-15 10:25:30", result.LatestDeadlock.Timestamp)
	assert.Len(t, result.LatestDeadlock.Transactions, 2)

	trx1 := result.LatestDeadlock.Transactions[0]
	assert.Equal(t, "12345", trx1.TrxID)
	assert.Equal(t, uint64(100), trx1.ThreadID)
	assert.Equal(t, "testdb.users", trx1.TableName)
	assert.Equal(t, "root", trx1.User)
	assert.Equal(t, "localhost", trx1.Host)
	assert.Equal(t, "UPDATE users SET name='test' WHERE id=1", trx1.Query)

	trx2 := result.LatestDeadlock.Transactions[1]
	assert.Equal(t, "12346", trx2.TrxID)
	assert.Equal(t, uint64(101), trx2.ThreadID)
	assert.Equal(t, "testdb.orders", trx2.TableName)
	assert.Equal(t, "root", trx2.User)
	assert.Equal(t, "UPDATE orders SET status='done' WHERE user_id=1", trx2.Query)
}

func TestParseInnoDBStatusNoDeadlock(t *testing.T) {
	raw := `
=====================================
INNODB MONITOR OUTPUT
=====================================
------------
TRANSACTIONS
------------
Trx id counter 100
--------
FILE I/O
--------
`
	result := ParseInnoDBStatus(raw)
	assert.Nil(t, result.LatestDeadlock)
}

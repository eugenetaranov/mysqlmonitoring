#!/usr/bin/env bash
set -euo pipefail

MYSQL="mysql -h mysql -uroot -pdemopass demodb --skip-column-names"

log() { echo "[workload] $(date +%H:%M:%S) $*"; }

# Wait for MySQL to be fully ready
until $MYSQL -e "SELECT 1" &>/dev/null; do
    log "Waiting for MySQL..."
    sleep 2
done
log "MySQL is ready, starting workload scenarios"

# ---------------------------------------------------------------------------
# Scenario 1: Long transaction (holds row lock for 15-20s)
# ---------------------------------------------------------------------------
scenario_long_transaction() {
    log "=== Scenario: Long Transaction ==="
    local sleep_time=$((15 + RANDOM % 6))
    $MYSQL -e "
        BEGIN;
        SELECT * FROM accounts WHERE id = 1 FOR UPDATE;
        SELECT SLEEP($sleep_time);
        ROLLBACK;
    " &>/dev/null &
    local pid=$!
    sleep "$((sleep_time + 1))"
    kill $pid 2>/dev/null || true
    wait $pid 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Scenario 2: Row lock contention (two sessions, same row)
# ---------------------------------------------------------------------------
scenario_row_lock() {
    log "=== Scenario: Row Lock Contention ==="
    # Session A: hold lock on row 3
    $MYSQL -e "
        BEGIN;
        UPDATE accounts SET balance = balance + 1 WHERE id = 3;
        SELECT SLEEP(12);
        ROLLBACK;
    " &>/dev/null &
    local pid_a=$!
    sleep 1

    # Session B: try to lock the same row (will block)
    $MYSQL -e "
        BEGIN;
        UPDATE accounts SET balance = balance - 1 WHERE id = 3;
        ROLLBACK;
    " &>/dev/null &
    local pid_b=$!

    sleep 13
    kill $pid_a $pid_b 2>/dev/null || true
    wait $pid_a $pid_b 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Scenario 3: Lock chain (A -> B -> C)
# ---------------------------------------------------------------------------
scenario_lock_chain() {
    log "=== Scenario: Lock Chain ==="
    # Session A: lock row 5, hold it
    $MYSQL -e "
        BEGIN;
        SELECT * FROM accounts WHERE id = 5 FOR UPDATE;
        SELECT SLEEP(15);
        ROLLBACK;
    " &>/dev/null &
    local pid_a=$!
    sleep 1

    # Session B: lock row 6, then try row 5 (blocks on A)
    $MYSQL -e "
        BEGIN;
        SELECT * FROM accounts WHERE id = 6 FOR UPDATE;
        SELECT * FROM accounts WHERE id = 5 FOR UPDATE;
        ROLLBACK;
    " &>/dev/null &
    local pid_b=$!
    sleep 1

    # Session C: try row 6 (blocks on B, which blocks on A)
    $MYSQL -e "
        BEGIN;
        SELECT * FROM accounts WHERE id = 6 FOR UPDATE;
        ROLLBACK;
    " &>/dev/null &
    local pid_c=$!

    sleep 14
    kill $pid_a $pid_b $pid_c 2>/dev/null || true
    wait $pid_a $pid_b $pid_c 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Scenario 4: DDL blocked by DML (metadata lock)
# ---------------------------------------------------------------------------
scenario_ddl_blocked() {
    log "=== Scenario: DDL Blocked by DML ==="
    # Session A: hold a transaction open on orders
    $MYSQL -e "
        BEGIN;
        SELECT * FROM orders WHERE id = 1 FOR UPDATE;
        SELECT SLEEP(12);
        ROLLBACK;
    " &>/dev/null &
    local pid_a=$!
    sleep 1

    # Session B: ALTER TABLE will block on metadata lock
    $MYSQL -e "
        ALTER TABLE orders ADD COLUMN IF NOT EXISTS temp_col INT DEFAULT NULL;
    " &>/dev/null &
    local pid_b=$!

    sleep 13
    kill $pid_a $pid_b 2>/dev/null || true
    wait $pid_a $pid_b 2>/dev/null || true

    # Clean up the temp column if it was added
    $MYSQL -e "ALTER TABLE orders DROP COLUMN IF EXISTS temp_col;" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Scenario 5: Deadlock provocation
# ---------------------------------------------------------------------------
scenario_deadlock() {
    log "=== Scenario: Deadlock ==="
    # Session A: lock row 7, then try row 8
    $MYSQL -e "
        BEGIN;
        UPDATE accounts SET balance = balance + 1 WHERE id = 7;
        SELECT SLEEP(3);
        UPDATE accounts SET balance = balance + 1 WHERE id = 8;
        ROLLBACK;
    " &>/dev/null &
    local pid_a=$!

    # Session B: lock row 8, then try row 7 (opposite order)
    $MYSQL -e "
        BEGIN;
        UPDATE accounts SET balance = balance + 1 WHERE id = 8;
        SELECT SLEEP(3);
        UPDATE accounts SET balance = balance + 1 WHERE id = 7;
        ROLLBACK;
    " &>/dev/null &
    local pid_b=$!

    sleep 8
    kill $pid_a $pid_b 2>/dev/null || true
    wait $pid_a $pid_b 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Scenario 6: Multiple concurrent blockers (different tables)
# ---------------------------------------------------------------------------
scenario_multi_blocker() {
    log "=== Scenario: Multiple Concurrent Blockers ==="
    # Blocker A: hold lock on accounts row 2
    $MYSQL -e "
        BEGIN;
        UPDATE accounts SET balance = balance + 10 WHERE id = 2;
        SELECT SLEEP(15);
        ROLLBACK;
    " &>/dev/null &
    local pid_a=$!
    sleep 1

    # Blocker B: hold lock on accounts row 4
    $MYSQL -e "
        BEGIN;
        SELECT * FROM accounts WHERE id = 4 FOR UPDATE;
        SELECT SLEEP(15);
        ROLLBACK;
    " &>/dev/null &
    local pid_b=$!
    sleep 1

    # Waiter 1: blocked by A
    $MYSQL -e "
        BEGIN;
        UPDATE accounts SET balance = balance - 5 WHERE id = 2;
        ROLLBACK;
    " &>/dev/null &
    local pid_c=$!

    # Waiter 2: also blocked by A
    $MYSQL -e "
        BEGIN;
        SELECT * FROM accounts WHERE id = 2 FOR UPDATE;
        ROLLBACK;
    " &>/dev/null &
    local pid_d=$!

    # Waiter 3: blocked by B
    $MYSQL -e "
        BEGIN;
        UPDATE accounts SET balance = balance + 1 WHERE id = 4;
        ROLLBACK;
    " &>/dev/null &
    local pid_e=$!

    sleep 14
    kill $pid_a $pid_b $pid_c $pid_d $pid_e 2>/dev/null || true
    wait $pid_a $pid_b $pid_c $pid_d $pid_e 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Main loop — run scenarios overlapping for constant activity
# ---------------------------------------------------------------------------
round=0
while true; do
    round=$((round + 1))
    log ">>> Round $round starting <<<"

    # Launch multiple scenarios concurrently so there are always locks visible
    scenario_long_transaction &
    sleep 2
    scenario_row_lock &
    sleep 2
    scenario_lock_chain &
    sleep 2
    scenario_multi_blocker &
    sleep 2
    scenario_ddl_blocked &
    sleep 2
    scenario_deadlock &

    # Wait for all background scenarios to finish
    wait 2>/dev/null || true

    log ">>> Round $round complete <<<"
    sleep 1
done

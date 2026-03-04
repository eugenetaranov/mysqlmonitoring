#!/usr/bin/env python3
"""
Workload generator that creates constant lock contention, long transactions,
deadlocks, and DDL conflicts against a MySQL demo instance.
"""

import threading
import time
import random
import logging
import mysql.connector

logging.basicConfig(
    level=logging.INFO,
    format="[workload] %(asctime)s %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger(__name__)

DSN = dict(host="mysql", port=3306, user="root", password="demopass", database="demodb")


def conn():
    return mysql.connector.connect(**DSN)


def wait_for_mysql():
    while True:
        try:
            c = conn()
            c.ping()
            c.close()
            log.info("MySQL is ready")
            return
        except Exception:
            log.info("Waiting for MySQL...")
            time.sleep(2)


# ---------------------------------------------------------------------------
# Scenarios
# ---------------------------------------------------------------------------

def long_transaction():
    """Hold a row lock for 12-18s, simulating a slow app query."""
    log.info("=== Long Transaction ===")
    hold = random.randint(12, 18)
    c = conn()
    cur = c.cursor()
    cur.execute("BEGIN")
    cur.execute("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")
    cur.fetchall()
    time.sleep(hold)
    cur.execute("ROLLBACK")
    cur.close()
    c.close()


def row_lock_contention():
    """Two sessions fight over the same row."""
    log.info("=== Row Lock Contention ===")
    hold = random.randint(10, 14)

    def blocker():
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        cur.execute("UPDATE accounts SET balance = balance + 1 WHERE id = 3")
        time.sleep(hold)
        cur.execute("ROLLBACK")
        cur.close()
        c.close()

    def waiter():
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        cur.execute("UPDATE accounts SET balance = balance - 1 WHERE id = 3")
        cur.execute("ROLLBACK")
        cur.close()
        c.close()

    t1 = threading.Thread(target=blocker)
    t1.start()
    time.sleep(1)
    t2 = threading.Thread(target=waiter)
    t2.start()
    t1.join()
    t2.join()


def lock_chain():
    """A -> B -> C chain: three sessions, cascading blocks."""
    log.info("=== Lock Chain ===")
    hold = random.randint(12, 16)
    ready_b = threading.Event()
    ready_c = threading.Event()

    def session_a():
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        cur.execute("SELECT * FROM accounts WHERE id = 5 FOR UPDATE")
        cur.fetchall()
        ready_b.set()
        time.sleep(hold)
        cur.execute("ROLLBACK")
        cur.close()
        c.close()

    def session_b():
        ready_b.wait()
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        cur.execute("SELECT * FROM accounts WHERE id = 6 FOR UPDATE")
        cur.fetchall()
        ready_c.set()
        # This blocks on A
        cur.execute("SELECT * FROM accounts WHERE id = 5 FOR UPDATE")
        cur.fetchall()
        cur.execute("ROLLBACK")
        cur.close()
        c.close()

    def session_c():
        ready_c.wait()
        time.sleep(0.5)
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        # This blocks on B
        cur.execute("SELECT * FROM accounts WHERE id = 6 FOR UPDATE")
        cur.fetchall()
        cur.execute("ROLLBACK")
        cur.close()
        c.close()

    threads = [
        threading.Thread(target=session_a),
        threading.Thread(target=session_b),
        threading.Thread(target=session_c),
    ]
    for t in threads:
        t.start()
    for t in threads:
        t.join()


def multi_blocker():
    """Two independent blockers with multiple waiters each."""
    log.info("=== Multiple Concurrent Blockers ===")
    hold = random.randint(12, 16)
    ready = threading.Event()

    def blocker_a():
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        cur.execute("UPDATE accounts SET balance = balance + 10 WHERE id = 2")
        ready.set()
        time.sleep(hold)
        cur.execute("ROLLBACK")
        cur.close()
        c.close()

    def blocker_b():
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        cur.execute("SELECT * FROM accounts WHERE id = 4 FOR UPDATE")
        cur.fetchall()
        time.sleep(hold)
        cur.execute("ROLLBACK")
        cur.close()
        c.close()

    def waiter_a1():
        ready.wait()
        time.sleep(0.5)
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        cur.execute("UPDATE accounts SET balance = balance - 5 WHERE id = 2")
        cur.execute("ROLLBACK")
        cur.close()
        c.close()

    def waiter_a2():
        ready.wait()
        time.sleep(1)
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        cur.execute("SELECT * FROM accounts WHERE id = 2 FOR UPDATE")
        cur.fetchall()
        cur.execute("ROLLBACK")
        cur.close()
        c.close()

    def waiter_b1():
        ready.wait()
        time.sleep(0.5)
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        cur.execute("UPDATE accounts SET balance = balance + 1 WHERE id = 4")
        cur.execute("ROLLBACK")
        cur.close()
        c.close()

    threads = [
        threading.Thread(target=blocker_a),
        threading.Thread(target=blocker_b),
        threading.Thread(target=waiter_a1),
        threading.Thread(target=waiter_a2),
        threading.Thread(target=waiter_b1),
    ]
    for t in threads:
        t.start()
    for t in threads:
        t.join()


def ddl_blocked():
    """DDL (ALTER TABLE) blocked by an open DML transaction."""
    log.info("=== DDL Blocked by DML ===")
    hold = random.randint(10, 14)
    ready = threading.Event()

    def dml_holder():
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        cur.execute("SELECT * FROM orders WHERE id = 1 FOR UPDATE")
        cur.fetchall()
        ready.set()
        time.sleep(hold)
        cur.execute("ROLLBACK")
        cur.close()
        c.close()

    def ddl_runner():
        ready.wait()
        time.sleep(0.5)
        c = conn()
        cur = c.cursor()
        try:
            cur.execute("ALTER TABLE orders ADD COLUMN IF NOT EXISTS temp_col INT DEFAULT NULL")
        except Exception:
            pass
        cur.close()
        c.close()

    t1 = threading.Thread(target=dml_holder)
    t2 = threading.Thread(target=ddl_runner)
    t1.start()
    t2.start()
    t1.join()
    t2.join()

    # cleanup
    try:
        c = conn()
        cur = c.cursor()
        cur.execute("ALTER TABLE orders DROP COLUMN IF EXISTS temp_col")
        cur.close()
        c.close()
    except Exception:
        pass


def deadlock():
    """Provoke a deadlock by two sessions locking rows in opposite order.
    The winning session holds its transaction open so the monitor can
    display it long enough to be killed interactively.
    """
    log.info("=== Deadlock ===")
    ready_a = threading.Event()
    ready_b = threading.Event()
    hold = random.randint(15, 25)

    def session_a():
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        cur.execute("UPDATE accounts SET balance = balance + 1 WHERE id = 7")
        ready_a.set()
        ready_b.wait()
        time.sleep(0.5)
        try:
            cur.execute("UPDATE accounts SET balance = balance + 1 WHERE id = 8")
        except Exception:
            pass
        # Hold the connection open so the monitor can show it
        time.sleep(hold)
        try:
            cur.execute("ROLLBACK")
        except Exception:
            pass
        cur.close()
        c.close()

    def session_b():
        ready_a.wait()
        c = conn()
        cur = c.cursor()
        cur.execute("BEGIN")
        cur.execute("UPDATE accounts SET balance = balance + 1 WHERE id = 8")
        ready_b.set()
        time.sleep(0.5)
        try:
            cur.execute("UPDATE accounts SET balance = balance + 1 WHERE id = 7")
        except Exception:
            pass
        # Hold the connection open so the monitor can show it
        time.sleep(hold)
        try:
            cur.execute("ROLLBACK")
        except Exception:
            pass
        cur.close()
        c.close()

    t1 = threading.Thread(target=session_a)
    t2 = threading.Thread(target=session_b)
    t1.start()
    t2.start()
    t1.join()
    t2.join()


# ---------------------------------------------------------------------------
# Main loop — overlap scenarios for constant lock activity
# ---------------------------------------------------------------------------

def run_scenario(fn):
    """Run a scenario in a thread, swallowing exceptions."""
    try:
        fn()
    except Exception as e:
        log.warning("Scenario %s failed: %s", fn.__name__, e)


def main():
    wait_for_mysql()

    scenarios = [
        long_transaction,
        row_lock_contention,
        lock_chain,
        multi_blocker,
        ddl_blocked,
        deadlock,
    ]

    rnd = 0
    while True:
        rnd += 1
        log.info(">>> Round %d starting <<<", rnd)

        # Launch all scenarios concurrently with small staggers
        threads = []
        for i, sc in enumerate(scenarios):
            t = threading.Thread(target=run_scenario, args=(sc,), daemon=True)
            threads.append(t)
            t.start()
            time.sleep(random.uniform(1, 3))

        for t in threads:
            t.join(timeout=60)

        log.info(">>> Round %d complete <<<", rnd)
        time.sleep(1)


if __name__ == "__main__":
    main()

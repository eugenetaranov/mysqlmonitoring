USE demodb;

CREATE TABLE accounts (
    id INT PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    balance DECIMAL(10,2) NOT NULL DEFAULT 0.00
);

INSERT INTO accounts (id, name, balance) VALUES
(1, 'Alice',   1000.00),
(2, 'Bob',     2500.00),
(3, 'Charlie', 1800.00),
(4, 'Diana',   3200.00),
(5, 'Eve',      750.00),
(6, 'Frank',   4100.00),
(7, 'Grace',   1500.00),
(8, 'Hank',    2200.00),
(9, 'Ivy',      900.00),
(10, 'Jack',   3700.00);

CREATE TABLE orders (
    id INT PRIMARY KEY,
    account_id INT NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    amount DECIMAL(10,2) NOT NULL,
    FOREIGN KEY (account_id) REFERENCES accounts(id)
);

INSERT INTO orders (id, account_id, status, amount) VALUES
(1,  1, 'pending',   100.00),
(2,  1, 'completed', 250.00),
(3,  2, 'pending',   300.00),
(4,  2, 'shipped',   175.00),
(5,  3, 'pending',   420.00),
(6,  3, 'completed',  60.00),
(7,  4, 'pending',   800.00),
(8,  4, 'shipped',   150.00),
(9,  5, 'pending',    90.00),
(10, 5, 'completed', 200.00),
(11, 6, 'pending',   550.00),
(12, 6, 'shipped',   330.00),
(13, 7, 'pending',   410.00),
(14, 7, 'completed', 120.00),
(15, 8, 'pending',   275.00),
(16, 8, 'shipped',   680.00),
(17, 9, 'pending',   190.00),
(18, 9, 'completed',  75.00),
(19, 10, 'pending',  900.00),
(20, 10, 'shipped',  450.00);

-- Enable the MDL instrument so the M tab in mysqlmonitoring (and the
-- ddl_conflict detector's findPotentialBlockers helper) actually have
-- data to read. Off by default on MySQL 5.7 / 8.0 LTS; on by default
-- in 8.1+. This is idempotent — runs once on first container boot via
-- /docker-entrypoint-initdb.d.
UPDATE performance_schema.setup_instruments
   SET ENABLED='YES', TIMED='YES'
 WHERE NAME LIKE 'wait/lock/metadata%';

-- Database for the sysbench oltp_read_write baseline workload. The
-- sysbench container's prepare step creates tables but won't create
-- the database itself, and the severalnines/sysbench image doesn't
-- ship a mysql client to do it from there.
CREATE DATABASE IF NOT EXISTS sbtest;

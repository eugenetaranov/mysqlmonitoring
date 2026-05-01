## ADDED Requirements

### Requirement: Periodic CloudWatch RDS metric collection

The system SHALL run a CloudWatch metrics collector at a 60-second interval and SHALL publish a `CWMetrics` snapshot consumed by the TUI verdict line. The collector SHALL issue exactly one `GetMetricData` call per cycle covering all required metrics so the AWS-side cost is bounded to one round-trip per minute.

#### Scenario: Collected metrics on RDS

- **WHEN** the collector polls against an RDS (non-Aurora) instance
- **THEN** the resulting snapshot SHALL contain `CPUUtilization`, `FreeableMemory`, `ReadIOPS`, `WriteIOPS`, `ReadLatency`, `WriteLatency`, `DiskQueueDepth`
- **AND** when the instance is a replica, the snapshot SHALL also contain `ReplicaLag`

#### Scenario: Collected metrics on Aurora

- **WHEN** the collector polls against an Aurora instance
- **THEN** the snapshot SHALL contain the RDS metrics above
- **AND** SHALL also contain `DBLoad`, `DBLoadCPU`, `DBLoadNonCPU`
- **AND** when the instance has a replica role, SHALL contain `AuroraReplicaLag`

### Requirement: AWS authentication via SDK default credential chain

The collector SHALL authenticate using the AWS SDK default credential chain (environment variables, `~/.aws/credentials` profile, IAM instance role, EKS service account, etc.) and SHALL NOT introduce a `--aws-profile` flag. Credential resolution SHALL happen exactly once at startup.

#### Scenario: Credentials available on developer laptop

- **WHEN** the operator runs `mysqlmonitoring monitor` on a laptop with `~/.aws/credentials` configured for the default profile
- **THEN** the collector SHALL load credentials from that file via the SDK chain
- **AND** the chrome `[cw]` indicator SHALL render

#### Scenario: No credentials present

- **WHEN** the SDK default chain produces no usable credentials
- **THEN** the collector SHALL log a single startup notice on stderr containing the failure reason returned by the chain
- **AND** SHALL NOT poll
- **AND** the verdict line SHALL render without CW columns
- **AND** the chrome `[cw]` indicator SHALL be absent (not dim)

### Requirement: Target detection — region and instance ID

The collector SHALL determine its CloudWatch target (region, RDS instance ID) using the following order; the first match wins:

1. Explicit `--aws-region` and `--rds-instance` flags.
2. Hostname pattern `<id>.<rand>.<region>.rds.amazonaws.com` (RDS instance endpoint).
3. Hostname pattern `<cluster>.cluster-<rand>.<region>.rds.amazonaws.com` or the `cluster-ro-` reader variant (Aurora cluster endpoint).
4. None of the above → collector disables.

The detected `(region, instanceID)` SHALL be cached for the process lifetime; redetection SHALL NOT happen at runtime.

#### Scenario: Hostname-parsed RDS endpoint

- **WHEN** the operator connects to `prod-orders.abc123.us-east-1.rds.amazonaws.com`
- **AND** does not pass `--aws-region` or `--rds-instance`
- **THEN** the collector SHALL resolve `region=us-east-1` and `instanceID=prod-orders`

#### Scenario: Explicit flag overrides hostname

- **WHEN** the operator passes `--rds-instance=different-name --aws-region=eu-west-1`
- **AND** the connection hostname suggests `us-east-1`
- **THEN** the collector SHALL use `eu-west-1` and `different-name`

#### Scenario: Non-RDS hostname

- **WHEN** the operator connects to `127.0.0.1:13306` (the demo stack)
- **AND** no flags are passed
- **THEN** the collector SHALL disable with the notice "CloudWatch metrics unavailable: hostname does not match an RDS endpoint pattern; pass --aws-region and --rds-instance to enable"

### Requirement: Counter-reset and missing-metric resilience

The collector SHALL tolerate metric responses that omit individual fields (e.g. `DBLoad` missing on a non-Aurora instance) without erroring the whole snapshot. Missing fields SHALL be reported as zero values; the consumer SHALL distinguish via a per-field `Has*` boolean or non-zero-value check.

#### Scenario: Aurora-only metric on RDS

- **WHEN** the collector queries `DBLoad` on a non-Aurora RDS instance
- **AND** CloudWatch returns no datapoints for that metric
- **THEN** `CWMetrics.DBLoad` SHALL be 0
- **AND** the verdict-line renderer SHALL NOT show the DBLoad gauge

## ADDED Requirements

### Requirement: Application tag resolution per session

The system SHALL resolve an application tag for each sampled session using the following ordered sources, taking the first non‑empty value:

1. A `service` key parsed from a leading sqlcommenter SQL comment in the session's current statement (e.g., `/* service='checkout', route='POST /cart' */ SELECT …`).
2. The `program_name` value from `performance_schema.session_connect_attrs` for the session's processlist id.
3. The literal string `unknown`.

#### Scenario: sqlcommenter comment present

- **WHEN** a session's current statement begins with `/* service='checkout', route='POST /cart' */ SELECT 1`
- **THEN** the resolved app tag SHALL be `checkout`

#### Scenario: connect_attrs program_name present

- **WHEN** a session has no leading SQL comment but `session_connect_attrs` contains `program_name = 'orders-api'`
- **THEN** the resolved app tag SHALL be `orders-api`

#### Scenario: No source available

- **WHEN** a session has neither a sqlcommenter comment nor a `program_name` connect attribute
- **THEN** the resolved app tag SHALL be `unknown`

### Requirement: Tag propagation to aggregations

The system SHALL attach the resolved app tag to every session sample and SHALL make per‑(digest, app) and per‑(wait‑class, app) breakdowns available alongside the global aggregations defined in `digest-sampling` and `wait-events`.

#### Scenario: Filter top SQL by app

- **WHEN** a top‑SQL aggregation is requested with filter `app='checkout'`
- **THEN** only digests with at least one session sample tagged `checkout` SHALL appear
- **AND** their AAS SHALL be computed solely from samples tagged `checkout`

### Requirement: SQL comment parsing safety

The system SHALL parse only a single leading sqlcommenter‑style block comment, SHALL NOT execute or interpret embedded SQL within the comment, and SHALL bound the parsed comment length to 1024 bytes. Malformed comments SHALL be ignored without raising an error.

#### Scenario: Oversized comment

- **WHEN** a session's statement begins with a `/* … */` comment whose body exceeds 1024 bytes
- **THEN** the parser SHALL return no tags
- **AND** SHALL fall back to `program_name`

#### Scenario: Malformed key=value pair

- **WHEN** a comment contains `service=` with no value, or unbalanced quoting
- **THEN** the parser SHALL skip that pair without aborting
- **AND** SHALL still return any well‑formed pairs from the same comment

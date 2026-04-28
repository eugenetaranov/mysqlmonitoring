## ADDED Requirements

### Requirement: M tab opens the MDL view in list mode

Pressing `M` from any tab SHALL switch the TUI to `ViewMDL` in list mode. The list SHALL show the top-N tables ranked by pending-queue depth, with columns for table name, pending count, granted count, lock-type buckets, and oldest-waiter age.

#### Scenario: Pressing M from Overview

- **WHEN** the user is on the Overview tab and presses `M`
- **THEN** the rendered view SHALL be the MDL list

### Requirement: Detail mode shows queue and holders

Selecting a table row in MDL list mode and pressing `enter` SHALL open MDL detail mode for that table. Detail mode SHALL render the FIFO waiter queue (oldest first) above the GRANTED holders.

#### Scenario: Drill from list

- **WHEN** the cursor is on `shop.orders` in the MDL list and the user presses `enter`
- **THEN** the view SHALL show `shop.orders` detail
- **AND** the QUEUE section SHALL render every PENDING entry with rank, age, PID, user@host, lock type, and SQL
- **AND** the HOLDERS section SHALL render every GRANTED entry

### Requirement: Drill from Overview's Hottest Tables panel

The "Hottest Tables" panel on the Overview tab SHALL be cursor-aware. Pressing `enter` on a row SHALL open MDL detail filtered to that table.

#### Scenario: Drill from Overview

- **WHEN** the user is on Overview, the cursor is on the Hottest Tables `shop.orders` row, and they press `enter`
- **THEN** the view SHALL change to `ViewMDL` in detail mode for `shop.orders`

### Requirement: Search for a PID in the queue

Pressing `/` in MDL detail mode SHALL prompt the user for a PID. If the PID is in the QUEUE, the cursor SHALL jump to that row.

#### Scenario: Locate own ALTER in a long queue

- **WHEN** the user enters `/8821` in detail mode and PID 8821 is at queue position 47
- **THEN** the cursor SHALL move to row 47

### Requirement: Graceful degradation when MDL instrument is off

When `Capabilities().MDLAvailable` is false, MDL list and detail modes SHALL render a single dim notice line containing the SQL to enable the instrument, instead of empty boxes.

#### Scenario: Instrument disabled

- **WHEN** the user opens MDL list mode and the probe reported the instrument off
- **THEN** the view SHALL render a notice with the `UPDATE setup_instruments` command
- **AND** SHALL NOT show empty list/detail chrome

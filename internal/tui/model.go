package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/detector"
	"github.com/eugenetaranov/mysqlmonitoring/internal/explain"
	"github.com/eugenetaranov/mysqlmonitoring/internal/insights"
	"github.com/eugenetaranov/mysqlmonitoring/internal/killer"
	"github.com/eugenetaranov/mysqlmonitoring/internal/monitor"
)

// ViewState identifies which top-level view the TUI is showing.
type ViewState int

const (
	ViewIssues ViewState = iota
	ViewLock
	ViewTop
	ViewExplain
	ViewIssueDetail
	ViewHelp
	ViewTables
	ViewOverview
	ViewMDL
)

// MDLMode picks list vs per-table detail inside the M tab.
type MDLMode uint8

const (
	MDLModeList MDLMode = iota
	MDLModeDetail
)

// ResultMsg carries a monitor result to the TUI.
type ResultMsg monitor.Result

// KillResultMsg carries the result of a kill operation.
type KillResultMsg struct {
	ConnectionID uint64
	Error        error
}

// PerfTickMsg fires periodically to refresh the sparkline trail and
// the top-SQL panel from the in-memory series. The TUI installs a
// ticker only if perf insights is enabled.
type PerfTickMsg time.Time

// ExplainMsg carries the result of a single explain.Engine.Run call.
type ExplainMsg struct {
	Result *explain.Result
	Err    error
}

// perfTickInterval is how often the sparkline + top panel refresh.
// One second strikes a balance between responsiveness and load on
// the in-memory series; the underlying samples only change every
// PollInterval anyway.
const perfTickInterval = time.Second

// Model is the bubbletea model for the TUI.
type Model struct {
	result      monitor.Result
	resultCh    <-chan monitor.Result
	killer      *killer.Killer
	cursor      int
	width       int
	height      int
	quitting    bool
	statusMsg   string
	confirmKill bool   // showing kill confirmation popup
	confirmPID  uint64 // PID to kill if confirmed

	// Bulk kill state for the Tables view: when set, the next y/Y
	// keypress kills every PID in confirmKillBatch and the popup
	// renders confirmKillTarget so the user knows what they're
	// signing off on.
	confirmKillBatch  []uint64
	confirmKillTarget string

	// Perf-insights view state. All fields are zero-valued and unused
	// when insights is nil.
	insights      *insights.Insights
	explainer     *explain.Engine
	view          ViewState
	loadWindow    time.Duration
	sparkTrail    []float64
	currentLoad   insights.LoadBreakdown
	topData       []insights.DigestSummary
	topCursor     int
	topSort       insights.SortKey
	topApp        string
	topSchema     string
	explainResult *explain.Result
	explainErr    error

	// Issues view state.
	issuesCursor      int
	issuesTableFilter string // when non-empty the Issues view shows only matching rows

	// Tables view state.
	tablesCursor int

	// Overview view state. loadGrouping cycles via u/h/s and
	// determines both the displayed grouping and which Top-SQL
	// drill filter (m.topUser / m.topHost / m.topSchema) gets
	// set on enter. overviewCursor selects within the load panel.
	loadGrouping   insights.GroupKey
	overviewCursor int
	topUser        string
	topHost        string

	// MDL view state. mdlMode toggles between the hottest-tables
	// list and a per-table detail view. mdlTableFilter is the
	// (schema, name) the detail view focuses on, set either by
	// drill-down from Overview's Hottest Tables panel or by
	// pressing enter on a list row.
	mdlMode          MDLMode
	mdlTableSchema   string
	mdlTableName     string
	mdlListCursor    int // selected row in list mode
	mdlQueueCursor   int // selected row in detail's QUEUE panel
	mdlBlockerFilter bool // when true, HOLDERS panel filters to entries that block the QUEUE cursor

	// helpReturn remembers which view to restore when the help
	// overlay is dismissed.
	helpReturn ViewState
}

// NewModel creates a new TUI model. Pass nil for insightsRef and
// explainer to disable the perf-insights views entirely.
//
// The default launch view is Overview — the operator's first question
// is "is the DB OK?", not "show me the lock tree". Pressing I goes
// straight to Issues; the old behaviour is one keystroke away.
func NewModel(resultCh <-chan monitor.Result, k *killer.Killer, insightsRef *insights.Insights, explainer *explain.Engine) Model {
	return Model{
		resultCh:     resultCh,
		killer:       k,
		insights:     insightsRef,
		explainer:    explainer,
		loadWindow:   time.Hour,
		view:         ViewOverview,
		loadGrouping: insights.GroupKeyUser,
	}
}

// waitForResult returns a tea.Cmd that waits for the next monitor result.
func (m Model) waitForResult() tea.Cmd {
	return func() tea.Msg {
		result, ok := <-m.resultCh
		if !ok {
			return tea.Quit()
		}
		return ResultMsg(result)
	}
}

func (m Model) Init() tea.Cmd {
	if m.insights != nil {
		return tea.Batch(m.waitForResult(), perfTick())
	}
	return m.waitForResult()
}

// perfTick is the recurring tea.Cmd that emits PerfTickMsg every
// perfTickInterval. The model re-arms it on each tick.
func perfTick() tea.Cmd {
	return tea.Tick(perfTickInterval, func(t time.Time) tea.Msg { return PerfTickMsg(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case ResultMsg:
		m.result = monitor.Result(msg)
		if msg.Error != nil {
			m.statusMsg = fmt.Sprintf("Error: %v", msg.Error)
		} else {
			m.statusMsg = ""
		}
		// Clamp cursor to a blocker entry
		entries := buildTreeEntries(m.result.Snapshot.LockWaits, m.result.Snapshot)
		if m.cursor >= len(entries) {
			m.cursor = max(0, len(entries)-1)
		}
		// Snap to nearest blocker
		if len(entries) > 0 && !entries[m.cursor].isBlocker {
			for i := m.cursor; i >= 0; i-- {
				if entries[i].isBlocker {
					m.cursor = i
					break
				}
			}
		}
		// Clamp issues cursor to valid range as the issue set churns.
		if n := len(m.result.Issues); n == 0 {
			m.issuesCursor = 0
		} else if m.issuesCursor >= n {
			m.issuesCursor = n - 1
		}
		// Clamp tables cursor against the freshly aggregated table set.
		if n := len(buildTableRows(m.result.Issues, m.result.Snapshot)); n == 0 {
			m.tablesCursor = 0
		} else if m.tablesCursor >= n {
			m.tablesCursor = n - 1
		}
		return m, m.waitForResult()

	case KillResultMsg:
		if msg.Error != nil {
			m.statusMsg = fmt.Sprintf("Kill failed: %v", msg.Error)
		} else {
			m.statusMsg = fmt.Sprintf("Killed connection %d", msg.ConnectionID)
		}
		return m, nil

	case PerfTickMsg:
		if m.insights == nil {
			return m, nil
		}
		now := time.Time(msg)
		m.sparkTrail = computeTrail(m.insights, now, m.loadWindow)
		m.currentLoad = insights.Load(m.insights.Waits, now, m.loadWindow)
		m.topData = insights.TopSQL(m.insights.Registry, m.insights.Sessions, now,
			insights.TopSQLOptions{
				Window: m.loadWindow,
				Sort:   m.topSort,
				Limit:  50,
				App:    m.topApp,
				Schema: m.topSchema,
			})
		if m.topCursor >= len(m.topData) {
			m.topCursor = max(0, len(m.topData)-1)
		}
		return m, perfTick()

	case ExplainMsg:
		m.explainResult = msg.Result
		m.explainErr = msg.Err
		return m, nil
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Handle confirmation popup keys first.
	if m.confirmKill {
		switch msg.String() {
		case "y", "Y":
			m.confirmKill = false
			k := m.killer

			// Bulk path: kill every PID in confirmKillBatch in
			// sequence. We surface the first error in the status
			// bar but always attempt every PID — partially-applied
			// bulk kills are noisy but recoverable, refusing the
			// remainder is worse.
			if pids := m.confirmKillBatch; len(pids) > 0 {
				target := m.confirmKillTarget
				m.confirmKillBatch = nil
				m.confirmKillTarget = ""
				return m, func() tea.Msg {
					var firstErr error
					ctx := context.Background()
					for _, pid := range pids {
						if err := k.Kill(ctx, pid); err != nil && firstErr == nil {
							firstErr = err
						}
					}
					return KillResultMsg{
						ConnectionID: uint64(len(pids)),
						Error:        wrapBulkErr(target, firstErr),
					}
				}
			}

			pid := m.confirmPID
			return m, func() tea.Msg {
				err := k.Kill(context.Background(), pid)
				return KillResultMsg{ConnectionID: pid, Error: err}
			}
		default:
			// Any other key cancels
			m.confirmKill = false
			m.confirmKillBatch = nil
			m.confirmKillTarget = ""
			m.statusMsg = "Kill cancelled"
			return m, nil
		}
	}

	key := msg.String()

	// Help overlay: any key dismisses it without taking action.
	// q closes the help instead of quitting; ctrl+c remains the
	// universal escape hatch and exits the program from anywhere.
	if m.view == ViewHelp {
		if key == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		m.view = m.helpReturn
		return m, nil
	}

	// View-level navigation that works from anywhere.
	switch key {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "?":
		m.helpReturn = m.view
		m.view = ViewHelp
		return m, nil
	case "esc":
		switch m.view {
		case ViewExplain:
			m.view = ViewTop
			m.explainResult = nil
			m.explainErr = nil
			return m, nil
		case ViewMDL:
			// Detail → list → Overview.
			if m.mdlMode == MDLModeDetail {
				m.mdlMode = MDLModeList
				m.mdlQueueCursor = 0
				return m, nil
			}
			m.view = ViewOverview
			return m, nil
		case ViewTop, ViewLock, ViewTables, ViewIssues:
			m.view = ViewOverview
			return m, nil
		case ViewIssueDetail:
			m.view = ViewIssues
			return m, nil
		}
	case "tab":
		// Cycle Overview → Issues → Tables → Lock → Overview.
		switch m.view {
		case ViewOverview:
			m.view = ViewIssues
			return m, nil
		case ViewIssues:
			m.view = ViewTables
			return m, nil
		case ViewTables:
			m.view = ViewLock
			return m, nil
		case ViewLock:
			m.view = ViewOverview
			return m, nil
		}
	case "t":
		if m.insights != nil {
			m.view = ViewTop
			return m, nil
		}
	case "O":
		m.view = ViewOverview
		return m, nil
	case "I":
		m.view = ViewIssues
		return m, nil
	case "L":
		m.view = ViewLock
		return m, nil
	case "B":
		m.view = ViewTables
		return m, nil
	case "M":
		m.view = ViewMDL
		m.mdlMode = MDLModeList
		m.mdlListCursor = 0
		return m, nil
	}

	// View-specific keys.
	switch m.view {
	case ViewOverview:
		return m.handleOverviewKey(key)
	case ViewIssues:
		return m.handleIssuesKey(key)
	case ViewIssueDetail:
		return m.handleIssueDetailKey(key)
	case ViewTop:
		return m.handleTopKey(key)
	case ViewTables:
		return m.handleTablesKey(key)
	case ViewMDL:
		return m.handleMDLKey(key)
	case ViewExplain:
		return m, nil
	}

	// ViewLock — original lock-tree navigation.
	switch key {
	case "up", "k":
		entries := buildTreeEntries(m.result.Snapshot.LockWaits, m.result.Snapshot)
		for i := m.cursor - 1; i >= 0; i-- {
			if entries[i].isBlocker {
				m.cursor = i
				break
			}
		}
		return m, nil

	case "down", "j":
		entries := buildTreeEntries(m.result.Snapshot.LockWaits, m.result.Snapshot)
		for i := m.cursor + 1; i < len(entries); i++ {
			if entries[i].isBlocker {
				m.cursor = i
				break
			}
		}
		return m, nil

	case "K":
		if m.killer == nil {
			return m, nil
		}
		entries := buildTreeEntries(m.result.Snapshot.LockWaits, m.result.Snapshot)
		if m.cursor >= len(entries) || len(entries) == 0 {
			return m, nil
		}
		m.confirmKill = true
		m.confirmPID = entries[m.cursor].pid
		return m, nil
	}

	return m, nil
}

// handleIssuesKey processes navigation/actions in the Issues table.
func (m Model) handleIssuesKey(key string) (tea.Model, tea.Cmd) {
	rows := visibleIssueRows(m.result.Issues, m.result.Snapshot, m.issuesTableFilter)
	switch key {
	case "up", "k":
		if m.issuesCursor > 0 {
			m.issuesCursor--
		}
		return m, nil
	case "down", "j":
		if m.issuesCursor < len(rows)-1 {
			m.issuesCursor++
		}
		return m, nil
	case "g":
		m.issuesCursor = 0
		return m, nil
	case "G":
		if len(rows) > 0 {
			m.issuesCursor = len(rows) - 1
		}
		return m, nil
	case "enter", "v":
		if len(rows) > 0 {
			m.view = ViewIssueDetail
		}
		return m, nil
	case "/":
		// Clear the table filter set by drilling in from Tables.
		if m.issuesTableFilter != "" {
			m.issuesTableFilter = ""
			m.issuesCursor = 0
		}
		return m, nil
	case "y":
		if m.issuesCursor < len(rows) {
			m.statusMsg = yankToClipboard(rows[m.issuesCursor].query)
		}
		return m, nil
	case "K":
		if m.killer == nil || m.issuesCursor >= len(rows) {
			return m, nil
		}
		row := rows[m.issuesCursor]
		if !row.killable() {
			m.statusMsg = "selected issue has no killable PID"
			return m, nil
		}
		m.confirmKill = true
		m.confirmPID = row.pid
		return m, nil
	}
	return m, nil
}

// handleTablesKey processes navigation/actions in the Tables panel.
func (m Model) handleTablesKey(key string) (tea.Model, tea.Cmd) {
	rows := buildTableRows(m.result.Issues, m.result.Snapshot)
	switch key {
	case "up", "k":
		if m.tablesCursor > 0 {
			m.tablesCursor--
		}
		return m, nil
	case "down", "j":
		if m.tablesCursor < len(rows)-1 {
			m.tablesCursor++
		}
		return m, nil
	case "g":
		m.tablesCursor = 0
		return m, nil
	case "G":
		if len(rows) > 0 {
			m.tablesCursor = len(rows) - 1
		}
		return m, nil
	case "enter":
		if m.tablesCursor < len(rows) {
			m.issuesTableFilter = rows[m.tablesCursor].name
			m.view = ViewIssues
			m.issuesCursor = 0
		}
		return m, nil
	case "K":
		if m.killer == nil || m.tablesCursor >= len(rows) {
			return m, nil
		}
		row := rows[m.tablesCursor]
		if len(row.pids) == 0 {
			m.statusMsg = "no killable connections on this table"
			return m, nil
		}
		m.confirmKill = true
		m.confirmKillBatch = row.killPIDs()
		m.confirmKillTarget = row.name
		return m, nil
	}
	return m, nil
}

// visibleIssueRows applies the optional table filter to the row
// list. Drilling in from the Tables view sets the filter; '/'
// clears it. For rows without an extractable table the filter is
// matched against the categorised bucket label so the breakdown
// (idle / admin / truncated) drills in correctly.
func visibleIssueRows(issues []detector.Issue, snap db.Snapshot, filter string) []issueRow {
	rows := buildIssueRows(issues, snap)
	if filter == "" {
		return rows
	}
	out := rows[:0]
	for _, r := range rows {
		name := r.table
		if name == "" {
			name = bucketForUntableable(r.query)
		}
		if name == filter {
			out = append(out, r)
		}
	}
	return out
}

// wrapBulkErr decorates a bulk-kill error with the table name so the
// status bar's Kill failed message names what was being killed.
func wrapBulkErr(target string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("bulk kill on %s: %w", target, err)
}

// handleIssueDetailKey accepts y to yank, esc to back out, K to kill.
func (m Model) handleIssueDetailKey(key string) (tea.Model, tea.Cmd) {
	rows := buildIssueRows(m.result.Issues, m.result.Snapshot)
	if m.issuesCursor >= len(rows) {
		m.view = ViewIssues
		return m, nil
	}
	row := rows[m.issuesCursor]
	switch key {
	case "y":
		m.statusMsg = yankToClipboard(row.query)
		return m, nil
	case "K":
		if m.killer == nil || !row.killable() {
			m.statusMsg = "selected issue has no killable PID"
			return m, nil
		}
		m.confirmKill = true
		m.confirmPID = row.pid
		return m, nil
	}
	return m, nil
}

// handleOverviewKey processes Overview-tab navigation. The cursor
// moves within the Top AAS users panel (the only top-N panel with a
// selectable cursor in the new layout). j/k/g/G navigate; enter (or
// `u`) drills into Top SQL filtered by the cursor user. h and s
// remain bound — they jump to Top SQL too but without a filter,
// preserving the muscle-memory "show me by host / by schema" while
// the dedicated Top SQL tab handles the multi-dimension drill.
func (m Model) handleOverviewKey(key string) (tea.Model, tea.Cmd) {
	rows := m.overviewLoadRows()
	switch key {
	case "up", "k":
		if m.overviewCursor > 0 {
			m.overviewCursor--
		}
		return m, nil
	case "down", "j":
		if m.overviewCursor < len(rows)-1 {
			m.overviewCursor++
		}
		return m, nil
	case "g":
		m.overviewCursor = 0
		return m, nil
	case "G":
		if len(rows) > 0 {
			m.overviewCursor = len(rows) - 1
		}
		return m, nil
	case "enter", "u":
		// Drill into Top SQL with the user filter set from the
		// cursor row. Clears any prior host/schema filter so the
		// destination view shows a clean breakdown.
		if m.overviewCursor >= len(rows) || m.insights == nil {
			return m, nil
		}
		m.topUser = rows[m.overviewCursor].Group
		m.topHost = ""
		m.topSchema = ""
		m.view = ViewTop
		return m, nil
	case "h", "s":
		// The h/s grouping cycle from the prior layout has gone away,
		// but we keep the keys functional: jump to Top SQL where the
		// per-dimension breakdowns live. Operators retain "press
		// h to look at hosts" as a one-stroke move.
		if m.insights == nil {
			return m, nil
		}
		m.topUser, m.topHost, m.topSchema = "", "", ""
		m.view = ViewTop
		return m, nil
	}
	return m, nil
}

// overviewLoadRows returns the Top AAS users panel rows over the
// Overview's 60s window. Returns nil when insights is unavailable.
func (m Model) overviewLoadRows() []insights.GroupLoad {
	if m.insights == nil || m.insights.Sessions == nil {
		return nil
	}
	return insights.LoadByGroup(m.insights.Sessions, time.Now(), overviewWindow, insights.GroupKeyUser)
}

// handleTopKey processes keystrokes while the Top SQL panel is in
// focus. Navigation uses j/k, sort cycles via 's', EXPLAIN fires on
// 'e' or Enter against the highlighted digest.
func (m Model) handleTopKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.topCursor > 0 {
			m.topCursor--
		}
		return m, nil
	case "down", "j":
		if m.topCursor < len(m.topData)-1 {
			m.topCursor++
		}
		return m, nil
	case "s":
		// Cycle through sort keys: AAS → Calls → Latency → Rows → AAS.
		m.topSort = (m.topSort + 1) % 4
		return m, nil
	case "e", "enter":
		if m.explainer == nil || m.topCursor >= len(m.topData) {
			return m, nil
		}
		digest := m.topData[m.topCursor].Digest
		m.view = ViewExplain
		m.explainResult = nil
		m.explainErr = nil
		eng := m.explainer
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			res, err := eng.Run(ctx, digest)
			return ExplainMsg{Result: &res, Err: err}
		}
	}
	return m, nil
}

func (m Model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	v := tea.NewView(renderMain(m))
	v.AltScreen = true
	return v
}

// Run starts the TUI. insightsRef and explainer may both be nil to
// disable the perf-insights views; in that case the existing lock
// monitor experience is unchanged.
func Run(resultCh <-chan monitor.Result, database db.DB, insightsRef *insights.Insights, explainer *explain.Engine) error {
	k := killer.New(database)
	model := NewModel(resultCh, k, insightsRef, explainer)
	p := tea.NewProgram(model)
	_, err := p.Run()
	return err
}

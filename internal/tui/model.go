package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/eugenetaranov/mysqlmonitoring/internal/db"
	"github.com/eugenetaranov/mysqlmonitoring/internal/killer"
	"github.com/eugenetaranov/mysqlmonitoring/internal/monitor"
)

// ResultMsg carries a monitor result to the TUI.
type ResultMsg monitor.Result

// KillResultMsg carries the result of a kill operation.
type KillResultMsg struct {
	ConnectionID uint64
	Error        error
}

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
}

// NewModel creates a new TUI model.
func NewModel(resultCh <-chan monitor.Result, k *killer.Killer) Model {
	return Model{
		resultCh: resultCh,
		killer:   k,
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
	return m.waitForResult()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
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
		return m, m.waitForResult()

	case KillResultMsg:
		if msg.Error != nil {
			m.statusMsg = fmt.Sprintf("Kill failed: %v", msg.Error)
		} else {
			m.statusMsg = fmt.Sprintf("Killed connection %d", msg.ConnectionID)
		}
		return m, nil
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle confirmation popup keys first
	if m.confirmKill {
		switch msg.String() {
		case "y", "Y":
			m.confirmKill = false
			pid := m.confirmPID
			k := m.killer
			return m, func() tea.Msg {
				err := k.Kill(context.Background(), pid)
				return KillResultMsg{ConnectionID: pid, Error: err}
			}
		default:
			// Any other key cancels
			m.confirmKill = false
			m.statusMsg = "Kill cancelled"
			return m, nil
		}
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "up", "k":
		entries := buildTreeEntries(m.result.Snapshot.LockWaits, m.result.Snapshot)
		// Jump to previous blocker
		for i := m.cursor - 1; i >= 0; i-- {
			if entries[i].isBlocker {
				m.cursor = i
				break
			}
		}
		return m, nil

	case "down", "j":
		entries := buildTreeEntries(m.result.Snapshot.LockWaits, m.result.Snapshot)
		// Jump to next blocker
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

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	return renderMain(m)
}

// Run starts the TUI.
func Run(resultCh <-chan monitor.Result, database db.DB) error {
	k := killer.New(database)
	model := NewModel(resultCh, k)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

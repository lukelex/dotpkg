package wizard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lukelex/dotpkg/internal/reconcile"
	"github.com/lukelex/dotpkg/internal/resource"
)

type tickMsg time.Time
type checkedMsg struct {
	err             error
	display         string
	conflicts       []resource.ConfigConflict
	readyDependents []string
}
type actionMsg struct{ err error }
type utilityMsg struct{ err error }

type model struct {
	r                     *runtime
	choices               []reconcile.SelectionChoice
	page                  string
	cursor                int
	input                 int
	width, height, scroll int
	steps                 []Step
	statuses              map[string]string
	index                 int
	busy                  bool
	stop                  bool
	started               time.Time
	display, message      string
	conflicts             []resource.ConfigConflict
	conflict              int
	kept                  []string
	complete              bool
	err                   error
}

func newModel(r *runtime, choices []reconcile.SelectionChoice) *model {
	m := &model{r: r, choices: choices, page: "profile", width: 80, height: 24, statuses: map[string]string{}}
	if r.session.Profile == "server" {
		m.cursor = 1
	}
	if r.options.Resume {
		m.page = "review"
		m.steps = r.spec.ForProfile(r.session.Profile, r.options.PackagesOnly)
	}
	return m
}

func (m *model) Init() tea.Cmd          { return tick() }
func tick() tea.Cmd                     { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func (m *model) fail(err error) tea.Cmd { m.err = err; return tea.Quit }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		return m, tick()
	case checkedMsg:
		m.busy = false
		if m.stop {
			return m, tea.Quit
		}
		m.display = msg.display
		m.conflicts = msg.conflicts
		m.conflict = 0
		for _, id := range msg.readyDependents {
			m.statuses[id] = "ready"
		}
		if msg.err == nil && len(msg.conflicts) == 0 {
			return m, m.advance("ready")
		}
		if len(msg.conflicts) > 0 {
			m.message = "Choose how to handle each existing config."
			return m, nil
		}
		m.message = "Not ready yet. Follow the instructions, then check again."
		m.statuses[m.steps[m.index].ID] = "needs setup"
		if msg.err != nil {
			m.message += "\n" + clean(msg.err.Error())
		}
	case actionMsg:
		m.busy = false
		if m.stop {
			return m, tea.Quit
		}
		if msg.err != nil {
			m.statuses[m.steps[m.index].ID] = "failed"
			m.r.event(m.steps[m.index].ID, "failed")
			m.message = "The action failed: " + clean(msg.err.Error()) + "\nFix the cause, then press a to retry. Logs: " + filepath.Join(m.r.directory, "run.log")
			return m, nil
		}
		if len(m.steps[m.index].Check) > 0 {
			return m, m.check()
		}
		return m, m.advance("done")
	case utilityMsg:
		if msg.err != nil {
			m.message = clean(msg.err.Error())
		} else {
			m.message = "Done."
		}
	case tea.KeyMsg:
		key := msg.String()
		if key == "ctrl+c" || (key == "q" && m.page != "inputs") {
			if m.busy {
				m.stop = true
				m.message = "Cancellation requested. Waiting for the current operation to finish safely."
				return m, nil
			}
			return m, tea.Quit
		}
		if key == "pgdown" {
			m.scroll += max(1, m.height-10)
			return m, nil
		}
		if key == "pgup" {
			m.scroll = max(0, m.scroll-max(1, m.height-10))
			return m, nil
		}
		if m.busy {
			return m, nil
		}
		if key == "esc" && m.page != "run" && m.page != "done" {
			m.page = "profile"
			m.cursor = 0
			m.scroll = 0
			return m, nil
		}
		switch m.page {
		case "profile":
			if key == "up" || key == "down" {
				m.cursor = 1 - m.cursor
			}
			if key == "enter" {
				m.r.session.Profile = []string{"desktop", "server"}[m.cursor]
				m.page = "host"
				m.cursor = 0
				for i, h := range m.r.hosts() {
					if h == m.r.session.Host {
						m.cursor = i
					}
				}
			}
		case "host":
			hosts := m.r.hosts()
			m.navigate(key, len(hosts))
			if key == "enter" {
				m.r.session.Host = hosts[m.cursor]
				choices, err := m.r.reloadChoices()
				if err != nil {
					m.message = err.Error()
					return m, nil
				}
				m.choices = choices
				m.page = "choices"
				m.cursor = 0
				m.scroll = 0
			}
		case "choices":
			m.navigate(key, len(m.choices))
			if key == " " && len(m.choices) > 0 {
				m.choices[m.cursor].Selected = !m.choices[m.cursor].Selected
			}
			if key == "enter" {
				m.r.session.Selections = map[string]bool{}
				for _, c := range m.choices {
					m.r.session.Selections[c.Key] = c.Selected
				}
				m.input = 0
				m.page = "inputs"
				if len(m.r.spec.Inputs) == 0 {
					m.review()
				}
			}
		case "inputs":
			field := m.r.spec.Inputs[m.input]
			value := m.r.session.Inputs[field.ID]
			if len(field.Choices) > 0 {
				i := 0
				for n, v := range field.Choices {
					if v == value {
						i = n
					}
				}
				if key == "up" || key == "left" {
					i = (i + len(field.Choices) - 1) % len(field.Choices)
				}
				if key == "down" || key == "right" || key == " " {
					i = (i + 1) % len(field.Choices)
				}
				m.r.session.Inputs[field.ID] = field.Choices[i]
			} else if key == "backspace" {
				runes := []rune(value)
				if len(runes) > 0 {
					m.r.session.Inputs[field.ID] = string(runes[:len(runes)-1])
				}
			} else if msg.Type == tea.KeyRunes && len(value) < 512 {
				m.r.session.Inputs[field.ID] = value + clean(string(msg.Runes))
			}
			if key == "enter" {
				if strings.TrimSpace(m.r.session.Inputs[field.ID]) == "" {
					m.message = "Enter a value before continuing."
					return m, nil
				}
				m.message = ""
				m.input++
				if m.input == len(m.r.spec.Inputs) {
					m.review()
				}
			}
		case "review":
			if key == "r" {
				m.r.session.Deferred = map[string]bool{}
				m.message = "Deferred tasks will be checked again."
			}
			if key == "enter" {
				if err := m.r.begin(); err != nil {
					return m, m.fail(err)
				}
				m.page = "run"
				m.index = 0
				m.started = time.Now()
				m.scroll = 0
				return m, m.startStep()
			}
		case "run":
			step := m.steps[m.index]
			if len(m.conflicts) > 0 {
				if key == "b" {
					backup, err := m.conflicts[m.conflict].Backup()
					if err != nil {
						m.message = err.Error()
						return m, nil
					}
					m.r.event(step.ID, "backup: "+backup)
					m.conflict++
				}
				if key == "k" {
					m.kept = append(m.kept, m.conflicts[m.conflict].Target)
					m.conflict++
				}
				if m.conflict == len(m.conflicts) {
					return m, m.advance("reviewed")
				}
				if key == "enter" {
					return m, m.check()
				}
				return m, nil
			}
			if key == "d" && m.canDefer(step) {
				m.r.session.Deferred[step.ID] = true
				if err := m.r.save(); err != nil {
					return m, m.fail(err)
				}
				return m, m.advance("deferred")
			}
			if key == "l" {
				return m, m.showLogs()
			}
			if key == "o" && step.URL != "" {
				return m, openURL(m.r.expand(step.URL))
			}
			if key == "c" && m.display != "" {
				return m, copyText(m.display)
			}
			if key == "enter" {
				if step.Graphical && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
					m.message = "Log into a graphical session first, or defer this optional step."
					return m, nil
				}
				for _, dep := range step.DependsOn {
					if status, ok := m.statuses[dep]; !ok || status == "deferred" || status == "blocked" {
						m.message = "Resolve prerequisite " + dep + " on the next run."
						return m, nil
					}
				}
				if step.Manual {
					m.r.session.Confirmed[step.ID] = true
					if err := m.r.save(); err != nil {
						return m, m.fail(err)
					}
					return m, m.advance("confirmed")
				}
				return m, m.check()
			}
			if key == "a" && len(step.Action) > 0 {
				return m, m.action()
			}
		case "done":
			if key == "enter" {
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m *model) navigate(key string, n int) {
	if n == 0 {
		return
	}
	if key == "up" {
		m.cursor = (m.cursor + n - 1) % n
	}
	if key == "down" {
		m.cursor = (m.cursor + 1) % n
	}
}
func (m *model) review() {
	m.page = "review"
	m.steps = m.r.spec.ForProfile(m.r.session.Profile, m.r.options.PackagesOnly)
	m.scroll = 0
}

func (m *model) startStep() tea.Cmd {
	if m.stop {
		return tea.Quit
	}
	if m.index >= len(m.steps) {
		m.page = "done"
		m.complete = true
		if err := m.r.continuation(false); err != nil {
			return m.fail(err)
		}
		return nil
	}
	step := m.steps[m.index]
	m.message = ""
	m.display = ""
	m.conflicts = nil
	m.scroll = 0
	if m.r.session.Deferred[step.ID] && m.canDefer(step) {
		return m.advance("deferred")
	}
	if m.statuses[step.ID] == "ready" && len(step.Check) > 0 {
		return m.check()
	}
	for _, dep := range step.DependsOn {
		status, exists := m.statuses[dep]
		if !exists || status == "deferred" || status == "blocked" {
			m.message = "Waiting for prerequisite " + dep + ". Resolve it on the next run."
			m.statuses[step.ID] = "blocked"
			return nil
		}
	}
	if step.Graphical && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		m.message = "This step needs a graphical session. Log into your desktop; setup will reopen automatically. You may also defer this step."
		return nil
	}
	if step.Manual {
		if m.r.session.Confirmed[step.ID] {
			return m.advance("confirmed")
		}
		return nil
	}
	if len(step.Check) > 0 || step.Kind != "" {
		return m.check()
	}
	return m.action()
}

func (m *model) canDefer(step Step) bool {
	if !step.Optional {
		return false
	}
	for _, dependent := range m.steps {
		if m.r.session.Deferred[dependent.ID] || m.statuses[dependent.ID] == "ready" {
			continue
		}
		for _, dependency := range dependent.DependsOn {
			if dependency == step.ID {
				return false
			}
		}
	}
	return true
}

func (m *model) advance(status string) tea.Cmd {
	step := m.steps[m.index]
	m.statuses[step.ID] = status
	m.r.event(step.ID, status)
	m.index++
	return m.startStep()
}

func (m *model) check() tea.Cmd {
	step := m.steps[m.index]
	m.busy = true
	m.message = "Checking current state…"
	return func() tea.Msg {
		if step.Kind == "config-conflicts" {
			conflicts, err := m.r.conflicts()
			return checkedMsg{err: err, conflicts: conflicts}
		}
		display, _ := m.r.inspect(step.Display)
		if len(step.Check) == 0 {
			return checkedMsg{err: fmt.Errorf("run the action to continue"), display: display}
		}
		_, err := m.r.inspect(step.Check)
		var ready []string
		if err != nil && step.Optional {
			for _, dependent := range m.steps {
				for _, dep := range dependent.DependsOn {
					if dep == step.ID && len(dependent.Check) > 0 {
						if _, checkErr := m.r.inspect(dependent.Check); checkErr == nil {
							ready = append(ready, dependent.ID)
						}
					}
				}
			}
		}
		return checkedMsg{err: err, display: display, readyDependents: ready}
	}
}

func (m *model) action() tea.Cmd {
	step := m.steps[m.index]
	if len(step.Action) == 0 {
		return nil
	}
	for _, dep := range step.DependsOn {
		if status, ok := m.statuses[dep]; !ok || status == "deferred" || status == "blocked" {
			m.message = "Resolve prerequisite " + dep + " first."
			return nil
		}
	}
	if step.Graphical && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		m.message = "Log into a graphical session first."
		return nil
	}
	m.busy = true
	m.statuses[step.ID] = "running"
	m.message = "Running… Press Ctrl-C to stop after this operation."
	m.r.event(step.ID, "running")
	cmd := m.r.command(context.Background(), step.Action)
	if step.Interactive {
		// The external tool owns its password prompts. Do not record its output:
		// account tools may show tokens, recovery codes, or other credentials.
		handoff, err := m.r.handoff(step)
		if err != nil {
			return func() tea.Msg { return actionMsg{err} }
		}
		return tea.ExecProcess(handoff, func(err error) tea.Msg { return actionMsg{err} })
	}
	cmd.Stdout = m.r
	cmd.Stderr = m.r
	return func() tea.Msg { return actionMsg{cmd.Run()} }
}

func (m *model) showLogs() tea.Cmd {
	path := filepath.Join(m.r.directory, "run.log")
	if m.index < len(m.steps) {
		stagePath := filepath.Join(m.r.directory, m.steps[m.index].ID+".log")
		if _, err := os.Stat(stagePath); err == nil {
			path = stagePath
		}
	}
	pager, err := exec.LookPath("less")
	if err != nil {
		m.message = "Logs: " + path
		return nil
	}
	return tea.ExecProcess(exec.Command(pager, "--", path), func(err error) tea.Msg { return utilityMsg{err} })
}
func openURL(url string) tea.Cmd {
	return func() tea.Msg { cmd := exec.Command("xdg-open", url); return utilityMsg{cmd.Run()} }
}
func copyText(text string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			cmd = exec.Command("wl-copy")
		} else if os.Getenv("DISPLAY") != "" {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else {
			return utilityMsg{fmt.Errorf("clipboard unavailable in this terminal; select the displayed text manually")}
		}
		cmd.Stdin = strings.NewReader(text)
		return utilityMsg{cmd.Run()}
	}
}

var accent = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true)
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

func (m *model) View() string {
	width := max(20, min(m.width-6, 110))
	var b strings.Builder
	heading := func(title string) { b.WriteString(accent.Render(title) + "\n\n") }
	paragraph := func(text string) { b.WriteString(lipgloss.NewStyle().Width(width).Render(clean(text)) + "\n\n") }
	item := func(i int, label string) {
		marker := "  "
		if i == m.cursor {
			marker = "> "
		}
		b.WriteString(marker + label + "\n")
	}
	heading(m.r.spec.Title)
	b.WriteString(muted.Render("INSPECT  /  CHOOSE  /  REVIEW  /  INSTALL  /  READY") + "\n\n")
	switch m.page {
	case "profile":
		heading("Choose this machine's profile")
		paragraph(m.r.spec.Description)
		item(0, "Desktop — applications, desktop sessions, and services")
		item(1, "Server — headless tools and services")
	case "host":
		heading("Machine-specific configuration")
		for i, h := range m.r.hosts() {
			if h == "" {
				h = "No host overlay"
			}
			item(i, h)
		}
	case "choices":
		heading("Choose components")
		for i, c := range m.choices {
			check := "[ ] "
			if c.Selected {
				check = "[x] "
			}
			item(i, check+c.Label)
		}
		if len(m.choices) > 0 {
			paragraph("Includes: " + strings.Join(m.choices[m.cursor].Packages, ", "))
		} else {
			paragraph("This profile has no optional package choices.")
		}
	case "inputs":
		field := m.r.spec.Inputs[m.input]
		heading(field.Title)
		paragraph(field.Description)
		paragraph("> " + m.r.session.Inputs[field.ID] + "_")
		if len(field.Choices) > 0 {
			paragraph("Choices: " + strings.Join(field.Choices, " / "))
		}
	case "review":
		heading("Review before making changes")
		if m.r.options.Bootstrap {
			paragraph("Create starter manifest: " + m.r.options.Reconcile.ManifestPath + ". Installs the selected tools on an existing Arch Linux system.")
		}
		paragraph("Profile: " + m.r.session.Profile + "    Host: " + m.r.session.Host)
		var selected []string
		for _, c := range m.choices {
			if c.Selected {
				selected = append(selected, c.Label)
			}
		}
		paragraph("Components: " + strings.Join(selected, ", "))
		for _, input := range m.r.spec.Inputs {
			paragraph(input.Title + ": " + m.r.session.Inputs[input.ID])
		}
		for i, step := range m.steps {
			label := fmt.Sprintf("%02d  %s", i+1, step.Title)
			if m.r.session.Deferred[step.ID] {
				label += " (deferred)"
			}
			heading(label)
			paragraph(step.Description)
		}
		paragraph("Enter approves this plan. Exact package transactions are resolved during execution. Password and package-manager prompts temporarily take over the terminal. Existing config conflicts are reviewed before linking. Completed account checks are skipped on reruns.")
		if m.r.spec.ContinueOnLogin && m.r.session.Profile == "desktop" {
			paragraph("Pending setup will reopen in a terminal after graphical login until completed or explicitly deferred.")
		}
	case "run":
		for i, step := range m.steps {
			if i < m.index-1 || i > m.index+1 {
				continue
			}
			status := m.statuses[step.ID]
			if status == "" {
				status = "pending"
			}
			prefix := "  "
			if i == m.index {
				prefix = "> "
			}
			b.WriteString(fmt.Sprintf("%s%02d  %-12s %s\n", prefix, i+1, status, step.Title))
		}
		b.WriteString("\n")
		step := m.steps[m.index]
		heading(fmt.Sprintf("Step %d of %d: %s", m.index+1, len(m.steps), step.Title))
		paragraph(step.Description)
		paragraph(m.r.expand(step.Instructions))
		if step.URL != "" {
			paragraph(m.r.expand(step.URL))
		}
		if m.display != "" {
			paragraph(m.display)
		}
		if len(m.conflicts) > 0 && m.conflict < len(m.conflicts) {
			c := m.conflicts[m.conflict]
			paragraph("Existing: " + c.Target + "\nDesired link: " + c.Source + "\n\nb: back up existing path, then let reconciliation create the link\nk: keep existing path\nEnter: inspect again")
		}
		if m.busy {
			frames := []string{"|", "/", "-", "\\"}
			paragraph(frames[time.Now().Unix()%4] + " Working    elapsed " + time.Since(m.started).Round(time.Second).String())
			m.r.mu.Lock()
			activity := m.r.activity
			m.r.mu.Unlock()
			if activity != "" {
				paragraph(activity)
			}
		}
	case "done":
		heading("Installation finished")
		for _, step := range m.steps {
			paragraph(step.Title + ": " + m.statuses[step.ID])
		}
		if len(m.kept) > 0 {
			paragraph("Existing configs kept; readiness may require manual resolution:\n" + strings.Join(m.kept, "\n"))
		}
		paragraph("Logs: " + filepath.Join(m.r.directory, "run.log"))
		paragraph("Run the installer again to recheck your machine. Press r on its review screen to revisit deferred setup.")
	}
	if m.message != "" {
		paragraph(m.message)
	}
	lines := strings.Split(b.String(), "\n")
	available := max(3, m.height-5)
	offset := min(m.scroll, max(0, len(lines)-available))
	end := min(len(lines), offset+available)
	body := strings.Join(lines[offset:end], "\n")
	footer := "↑/↓ choose   Space toggle   Enter continue   Esc back   q quit"
	if m.page == "inputs" {
		footer = "Type / arrows edit   Enter continue   Esc back   Ctrl-C quit"
	}
	if m.page == "review" {
		footer = "Enter approve   r revisit deferred   Esc edit   q quit"
	}
	if m.page == "run" {
		footer = "Enter check/confirm   a run/retry   d defer optional   o open   c copy   l logs   q quit"
	}
	if m.page == "done" {
		footer = "Enter exit"
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(body) + "\n" + muted.Render(footer+"\nPgUp/PgDn scroll")
}

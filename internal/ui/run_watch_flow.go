// Copyright (c) 2026 Circle Internet Services, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
//
// SPDX-License-Identifier: MIT

package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/uuid"

	"github.com/CircleCI-Public/circleci-cli/clikit/ui/components"
	"github.com/CircleCI-Public/circleci-cli/clikit/ui/theme"
)

// Poll cadence for the watch flow. The wait starts at five seconds and backs
// off by one second per poll, up to thirty seconds. The clock redraws at least
// once a second regardless, so the display never looks frozen between polls.
const (
	RunWatchPollInterval    = 5 * time.Second
	RunWatchPollBackoff     = 1 * time.Second
	RunWatchMaxPollInterval = 30 * time.Second
	RunWatchBackoffDelay    = 1 * time.Minute

	// runWatchClockTick is the longest a clock tick ever waits. A tick usually
	// comes sooner: it is aimed at whichever figure on screen changes next (see
	// clockDelay).
	runWatchClockTick = time.Second
)

// Table column widths, in cells. A name longer than its column is truncated
// rather than allowed to push the status column out of line. The job column is
// two narrower than the workflow one to pay for the deeper indent, so both
// levels' status glyphs land in the same column while the names still nest.
const (
	runWatchWorkflowNameCol = 30
	runWatchJobNameCol      = 28
	runWatchStepNameCol     = 26
	runWatchStatusCol       = 12
)

// The footer's poll meter: how many cells it is drawn in, and the filled and
// empty cell. Six cells is enough motion to read as a countdown on the tightest
// interval without turning the footer into a progress bar.
const (
	runWatchMeterCells = 6
	runWatchMeterFull  = "▰"
	runWatchMeterEmpty = "▱"
)

// runWatchClockLayout is how a row's timing column renders a timestamp when the
// caller supplied no locale-specific layout: a plain 24-hour clock, paired with
// the compact duration beside it. A caller that consulted the locale passes the
// user's own hour cycle through RunWatchFlowOptions.ClockLayout instead.
const runWatchClockLayout = "15:04:05"

// RunWatchJob is one job row in the watch table. Symbol is the uncolored status
// glyph (the flow colors it when color is enabled) and Status the matching word,
// kept apart so the word can be padded into a fixed-width column without the
// glyph's width throwing the alignment off. Failed marks a job whose outcome is
// a failure, which is what --failfast trips on and what the final error's
// suggestions are built from.
//
// URL is the job's page in the CircleCI app, and StartedAt/EndedAt are its
// timings; the flow renders the name as a link and the timings as the row's
// trailing column. All three are supplied by the caller — this package stays
// independent of internal/apiclient and of the app-URL helpers the command layer
// owns — and all three may be empty: a queued job has not started yet, and a
// caller that could not resolve the app URL passes no links at all.
type RunWatchJob struct {
	ID        uuid.UUID
	Name      string
	URL       string
	Symbol    string
	Status    string
	Type      string
	StartedAt *time.Time
	EndedAt   *time.Time
	Failed    bool
	RunsDone  int
	RunsTotal int
	Steps     []RunWatchStep
}

// RunWatchStep is the current or most recently named step in one job execution.
type RunWatchStep struct {
	Execution int
	Done      bool
	Number    int
	StepNum   int
	Name      string
	Command   string
	Symbol    string
	Status    string
	URL       string
	StartedAt *time.Time
	EndedAt   *time.Time
}

// RunWatchFailedStep identifies a failed step whose output can be displayed.
type RunWatchFailedStep struct {
	JobID   uuid.UUID
	JobName string
	Step    RunWatchStep
}

// RunWatchWorkflow is one workflow row and the job rows nested under it. URL and
// the timings mean what they do on RunWatchJob; a workflow that has not ended has
// no EndedAt, and one that has not started has neither.
type RunWatchWorkflow struct {
	ID        uuid.UUID
	Name      string
	URL       string
	Symbol    string
	Status    string
	CreatedAt time.Time
	EndedAt   *time.Time
	Jobs      []RunWatchJob
}

// RunWatchError is one error the run carries in its own right, rather than the
// outcome of a job: a config that would not compile, or a dynamic-config
// continuation whose config was rejected. The flow does not draw these — they
// are read off the final state by the caller, which prints them with the summary
// once the run is over.
type RunWatchError struct {
	Type    string
	Message string
}

// RunWatchWarning is one non-fatal warning the run carries in its own right,
// the warning counterpart to RunWatchError.
type RunWatchWarning struct {
	Type        string
	Message     string
	Description string
	URL         string
}

// RunWatchState is one poll's worth of run state: the rows to draw, the run's
// own errors, whether the run has finished, and its derived display status once
// it has. Like the run-get item types this mirrors what the API client returns
// rather than embedding it, keeping this package independent of
// internal/apiclient — the caller's Fetch callback does the conversion.
//
// Done is about the run, not its workflows: AllWorkflowsEnded says the workflows
// are finished, which is a weaker thing (see pendingNote).
type RunWatchState struct {
	Workflows []RunWatchWorkflow
	Errors    []RunWatchError
	Warnings  []RunWatchWarning
	Done      bool

	// AllWorkflowsEnded reports that every workflow the run has produced so far
	// has reached a terminal phase — true of a dynamic-config run waiting on its
	// continuation, which is not done.
	AllWorkflowsEnded bool

	Outcome string
}

// FailedJobs returns every job across every workflow whose outcome is a failure,
// in the order they appear in the table.
func (s RunWatchState) FailedJobs() []RunWatchJob {
	var failed []RunWatchJob
	for _, wf := range s.Workflows {
		for _, j := range wf.Jobs {
			if j.Failed {
				failed = append(failed, j)
			}
		}
	}
	return failed
}

// RunWatchResult is the outcome of a completed RunWatchFlowModel run, read via
// Result() after tea.Program.Run() returns. Exactly one of Cancelled, TimedOut,
// FailFast, Err or "the run finished" is true; in the last case State.Outcome
// says how it finished. State is always the most recent poll, so the caller can
// report failed jobs whichever way the flow ended.
type RunWatchResult struct {
	State     RunWatchState
	Elapsed   time.Duration
	Cancelled bool
	TimedOut  bool
	FailFast  bool
	RerunWfID uuid.UUID
	Err       error
}

// RunWatchFlowOptions configures a RunWatchFlowModel. Fetch is the only required
// field: it returns the run's current state and is called once on start and then
// on every poll.
type RunWatchFlowOptions struct {
	RunID  uuid.UUID
	Branch string

	Color   bool
	Animate bool

	// ClockLayout is the Go time layout the timing column renders a row's
	// timestamps with — the caller passes the user's locale-aware layout (see
	// iostream.ClockLayout), this package having no business reading the
	// environment itself. Empty means the plain 24-hour default.
	ClockLayout string

	// Theme is the resolved color theme the table draws in — the palette the
	// --theme flag or the stored "theme" setting selected, with "auto" already
	// resolved against the terminal (see iostream.ResolvedTheme). Empty means the
	// house palette. It is ignored when Color is false, and when the theme itself
	// is a plain one ("ascii", "notty"): either way the table draws no escapes at
	// all.
	Theme string

	// FailFast ends the watch as soon as any job has failed, without waiting for
	// the rest of the run.
	FailFast bool
	NoFail   bool

	// Timeout ends the watch once this much time has elapsed. Zero means no
	// timeout.
	Timeout time.Duration

	// PollInterval and MaxPollInterval override the default poll cadence. Zero
	// means RunWatchPollInterval / RunWatchMaxPollInterval.
	PollInterval    time.Duration
	MaxPollInterval time.Duration

	Fetch           func(ctx context.Context) (RunWatchState, error)
	FetchStepOutput func(ctx context.Context, jobID uuid.UUID, execution, stepNum int) (string, error)
	Output          io.Writer
}

// async message types carrying poll results and clock ticks into the Update loop.
type (
	runWatchStateMsg struct {
		state RunWatchState
		err   error
	}
	// runWatchClockMsg carries the generation of the chain that armed it, so a
	// tick from a superseded schedule is dropped rather than acted on.
	runWatchClockMsg      struct{ gen int }
	runWatchStepOutputMsg struct {
		output string
		err    error
	}
	runWatchStepOutputReadyMsg struct {
		output string
		err    error
	}
	runWatchStepOutputPrintedMsg struct{ err error }
)

// runWatchOutputCommand writes step output while Bubble Tea has released the
// terminal. This keeps the inline watch frame from overwriting the output.
type runWatchOutputCommand struct {
	writer io.Writer
	output string
}

func (c *runWatchOutputCommand) Run() error {
	output := strings.Trim(c.output, "\r\n")
	if output == "" {
		return nil
	}
	_, err := io.WriteString(c.writer, output+"\n")
	return err
}

func (*runWatchOutputCommand) SetStdin(io.Reader) {}
func (c *runWatchOutputCommand) SetStdout(writer io.Writer) {
	if c.writer == nil {
		c.writer = writer
	}
}
func (*runWatchOutputCommand) SetStderr(io.Writer) {}

// runWatchKeys is the footer key hint set: poll again now, or stop watching.
// The table refreshes itself, so refresh is there for the widening poll interval
// alone — by the end of a long run the next update can be half a minute away,
// and the footer's countdown says so. ctrl+c and esc also quit but go
// unadvertised — the footer names the shortest form of each action, as the
// pickers' footers do.
var runWatchKeys = []key.Binding{components.BindFailed, components.BindRefresh, components.BindQuit}

// RunWatchFlowModel is the bubbletea program behind `circleci run watch` in an
// interactive terminal: a live table of the run's workflows and their jobs, each
// row carrying a status glyph, redrawn in place as the run progresses.
//
// It polls through the caller-supplied Fetch callback — first immediately, then
// on a widening interval — and ends itself when every workflow has reached a
// terminal phase, when --failfast trips on a failed job, or when the timeout
// elapses. r polls again straight away; q, esc and ctrl+c stop watching (the run
// itself keeps going). After Run() returns, read the outcome with Result(); the
// caller prints the final summary line and decides the exit code.
//
// The program is deliberately inline rather than full-screen: the final frame is
// the completed table, and the caller's summary line prints directly beneath it.
type RunWatchFlowModel struct {
	ctx     context.Context
	opts    RunWatchFlowOptions
	palette theme.Palette

	width  int
	height int
	scroll int
	start  time.Time
	spin   spinner.Model
	state  RunWatchState
	loaded bool

	// complete remembers terminal parallel rows already shown. A
	// complete execution gets one refresh in the table, then disappears while
	// the remaining executions continue.
	complete map[string]bool

	// The poll schedule. wait is the interval the countdown on screen is measured
	// against and nextWait the one after it. nextPoll is when the
	// wait expires, and is zero while a fetch is in flight.
	wait     time.Duration
	nextWait time.Duration
	nextPoll time.Time
	polling  bool
	pollTime time.Time

	// clockGen identifies the live clock chain. Arming a clock supersedes the
	// outstanding one, and a tick that arrives from an older generation is
	// ignored — so anything that changes the schedule can re-arm freely without
	// leaving a second chain running behind it. That is the bug this flow shipped
	// with: a poll timer abandoned by "r" fired anyway, and from then on the run
	// refreshed at twice the cadence its own countdown showed.
	clockGen int

	done   bool
	result RunWatchResult
	inMenu bool
	menu   components.SelectModel
	steps  []RunWatchFailedStep
	busy   bool
}

// NewRunWatchFlow returns a RunWatchFlowModel ready to pass to tea.NewProgram.
func NewRunWatchFlow(ctx context.Context, opts RunWatchFlowOptions) RunWatchFlowModel {
	if opts.PollInterval <= 0 {
		opts.PollInterval = RunWatchPollInterval
	}
	if opts.MaxPollInterval <= 0 {
		opts.MaxPollInterval = RunWatchMaxPollInterval
	}
	palette := resolvePalette(opts)
	spin := components.NewSpinner(opts.Color)
	// The shared spinner draws its glyph in the house accent, and has no theme of
	// its own to consult; override it with the table's, so the spinner that leads
	// the table is drawn in the same theme the table is.
	spin.Style = palette.Accent
	return RunWatchFlowModel{
		ctx:      ctx,
		opts:     opts,
		palette:  palette,
		start:    time.Now(),
		spin:     spin,
		nextWait: opts.PollInterval,
		complete: make(map[string]bool),
		// Init's first act is to fetch, so the model starts mid-poll.
		polling: true,
	}
}

// resolvePalette picks the theme the table draws in. Color off means the plain
// palette whatever theme was named — a terminal that asked for no escapes must
// not get them from the theme either — and an unset theme means the house
// palette rather than a lookup miss, so a caller that only ever sets Color keeps
// the table it had.
func resolvePalette(opts RunWatchFlowOptions) theme.Palette {
	if !opts.Color {
		return theme.PlainPalette()
	}
	if opts.Theme == "" {
		return theme.DefaultPalette()
	}
	return theme.PaletteFor(opts.Theme)
}

// Result returns the final outcome. Only valid after tea.Program.Run() returns.
func (m RunWatchFlowModel) Result() RunWatchResult { return m.result }

func (m RunWatchFlowModel) Init() tea.Cmd {
	// Init cannot alter the model, so the first clock is armed at the generation
	// the model already carries; every later arm supersedes it.
	clock := tea.Tick(m.clockDelay(), func(time.Time) tea.Msg { return runWatchClockMsg{gen: m.clockGen} })
	cmds := []tea.Cmd{m.cmdFetch(), clock}
	if m.opts.Animate {
		cmds = append(cmds, m.spin.Tick)
	}
	return tea.Batch(cmds...)
}

func (m RunWatchFlowModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.inMenu {
			updated, cmd := m.menu.Update(msg)
			m.menu = updated.(components.SelectModel)
			return m, cmd
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.onKey(msg)

	case runWatchStateMsg:
		return m.onState(msg)
	case runWatchStepOutputMsg:
		m.inMenu = false
		m.busy = true
		return m, func() tea.Msg {
			return runWatchStepOutputReadyMsg{output: msg.output, err: msg.err}
		}
	case runWatchStepOutputReadyMsg:
		output := msg.output
		if msg.err != nil {
			output = "Failed to fetch step output: " + msg.err.Error()
		}
		writer := m.opts.Output
		if writer == nil {
			writer = os.Stdout
		}
		return m, tea.Exec(&runWatchOutputCommand{writer: writer, output: output}, func(err error) tea.Msg {
			return runWatchStepOutputPrintedMsg{err: err}
		})
	case runWatchStepOutputPrintedMsg:
		m.busy = false
		if msg.err != nil {
			return m, tea.Println("Failed to print step output: " + msg.err.Error())
		}
		return m, nil

	case runWatchClockMsg:
		return m.onClock(msg)

	case spinner.TickMsg:
		if m.done {
			return m, nil
		}
		s, cmd := m.spin.Update(msg)
		m.spin = s
		return m, cmd
	}
	return m, nil
}

// onClock advances the display and, when the wait has expired, starts the next
// poll. Polling rides on the clock rather than a timer of its own, so the poll
// can only ever happen once the countdown on screen has actually run out.
func (m RunWatchFlowModel) onClock(msg runWatchClockMsg) (tea.Model, tea.Cmd) {
	if m.done || msg.gen != m.clockGen {
		return m, nil
	}
	if m.timedOut() {
		m.result.TimedOut = true
		return m.quit()
	}
	if m.waitExpired() {
		next, fetch := m.beginPoll()
		next, clock := next.armClock()
		return next, tea.Batch(fetch, clock)
	}
	next, clock := m.armClock()
	return next, clock
}

// waitExpired reports whether the scheduled wait has run out. A fetch already in
// flight (nextPoll zero) has nothing to wait for.
func (m RunWatchFlowModel) waitExpired() bool {
	return !m.polling && !m.nextPoll.IsZero() && !time.Now().Before(m.nextPoll)
}

// onKey handles the two things a watcher can do: poll again now, or stop
// watching. Quitting is not an error — the run carries on in CircleCI — but the
// caller reports it as a cancellation so a script cannot mistake an abandoned
// watch for a successful run.
func (m RunWatchFlowModel) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.inMenu {
		if key.Matches(msg, components.KeyEsc, components.BindQuit) {
			m.inMenu = false
			return m, nil
		}
		if key.Matches(msg, components.KeyCtrlC) {
			m.result.Cancelled = true
			return m.quit()
		}
		updated, cmd := m.menu.Update(msg)
		m.menu = updated.(components.SelectModel)
		if !m.menu.Done() {
			return m, cmd
		}
		return m, m.cmdStepOutput(m.steps[m.menu.Selected()])
	}
	switch {
	case key.Matches(msg, components.KeyCtrlC, components.KeyEsc, components.BindQuit):
		m.result.Cancelled = true
		return m.quit()
	case key.Matches(msg, components.BindRefresh):
		if m.done || m.polling {
			return m, nil
		}
		// A manual refresh resets the scheduled wait: the user is watching closely
		// again, so go back to the tightest cadence.
		m.nextWait = m.opts.PollInterval
		m.pollTime = time.Now()
		next, fetch := m.beginPoll()
		next, clock := next.armClock()
		return next, tea.Batch(fetch, clock)
	case key.Matches(msg, components.BindRerun):
		if m.opts.NoFail && m.result.State.Done && m.result.State.Outcome == "failed" {
			m.result.RerunWfID = m.failedWorkflowID()
			return m.quit()
		}
		return m, nil
	case key.Matches(msg, components.BindFailed):
		m.steps = m.state.FailedSteps()
		if len(m.steps) == 1 {
			return m, m.cmdStepOutput(m.steps[0])
		}
		if len(m.steps) > 1 {
			m.inMenu = true
			labels := make([]string, len(m.steps))
			icons := make([]string, len(m.steps))
			for i, step := range m.steps {
				labels[i] = fmt.Sprintf("%s — %s (#%d)", step.JobName, step.Step.Name, step.Step.StepNum)
				icons[i] = m.palette.StatusText("✗", "✗")
			}
			m.menu = components.NewSelectModel("Select a failed step", labels).
				WithIcons(icons).
				WithHeight(m.height)
		}
		return m, nil
	case key.Matches(msg, components.KeyUp, components.KeyDown):
		available := m.tableViewportHeight()
		rows := m.ViewTable()
		if len(rows) > available {
			delta := 1
			if key.Matches(msg, components.KeyUp) {
				delta = -1
			}
			maxOffset := max(0, len(rows)-max(1, available-1))
			page := max(1, m.height/3)
			m.scroll = min(maxOffset, max(0, m.scroll+delta*page))
			return m, nil
		}

	}
	return m, nil
}

func (m RunWatchFlowModel) tableViewportHeight() int {
	if m.height <= 0 {
		return 0
	}
	reserved := 2 // title and blank line below it
	if m.pendingNote() != "" {
		reserved++
	}
	if footer := m.footer(); footer != "" {
		reserved += strings.Count(footer, "\n") + 1
	}
	return max(0, m.height-reserved)
}

func (m RunWatchFlowModel) cmdStepOutput(step RunWatchFailedStep) tea.Cmd {
	return func() tea.Msg {
		if m.opts.FetchStepOutput == nil {
			return runWatchStepOutputMsg{err: errors.New("step output is unavailable")}
		}
		out, err := m.opts.FetchStepOutput(m.ctx, step.JobID, step.Step.Execution, step.Step.StepNum)
		return runWatchStepOutputMsg{output: out, err: err}
	}
}

func (s RunWatchState) FailedSteps() []RunWatchFailedStep {
	var out []RunWatchFailedStep
	for _, wf := range s.Workflows {
		for _, job := range wf.Jobs {
			for _, step := range job.Steps {
				if IsFailedStepStatus(step.Status) {
					out = append(out, RunWatchFailedStep{JobID: job.ID, JobName: job.Name, Step: step})
				}
			}
		}
	}
	return out
}

// IsFailedStepStatus reports whether a terminal step status represents a
// failure, including failures reported as timeouts or infrastructure errors.
func IsFailedStepStatus(status string) bool {
	switch status {
	case "failed", "timedout", "errored", "infrastructure_fail":
		return true
	default:
		return false
	}
}

// beginPoll marks a fetch as in flight — the countdown has nothing left to count
// down to — and returns the command that runs it.
func (m RunWatchFlowModel) beginPoll() (RunWatchFlowModel, tea.Cmd) {
	m.polling = true
	m.nextPoll = time.Time{}
	return m, m.cmdFetch()
}

// onState folds a completed poll into the model and decides whether the watch is
// over. A fetch failure ends the flow: watch has no partial state worth showing
// once the API stops answering, and a caller that kept polling through errors
// would hide an expired token behind a spinner.
func (m RunWatchFlowModel) onState(msg runWatchStateMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			m.result.Cancelled = true
		} else {
			m.result.Err = msg.err
		}
		return m.quit()
	}

	msg.state = m.expireCompletedExecutions(msg.state)
	m.state = msg.state
	m.loaded = true
	m.polling = false
	m.result.State = msg.state

	switch {
	case msg.state.Done:
		if m.opts.NoFail && msg.state.Outcome == "failed" && m.failedWorkflowID() != uuid.Nil {
			m.result.Elapsed = m.elapsed()
			m.done = false
			return m, nil
		}
		return m.quit()
	case m.opts.FailFast && len(msg.state.FailedJobs()) > 0:
		m.result.FailFast = true
		return m.quit()
	case m.timedOut():
		m.result.TimedOut = true
		return m.quit()
	}

	m.wait = m.nextWait
	m.nextPoll = time.Now().Add(m.wait)
	pollStart := m.start
	if !m.pollTime.IsZero() {
		pollStart = m.pollTime
	}
	if time.Since(pollStart) >= RunWatchBackoffDelay {
		m.nextWait = min(m.nextWait+RunWatchPollBackoff, m.opts.MaxPollInterval)
	}
	// Re-arm rather than leave it to the outstanding tick, which was aimed at a
	// schedule that predates this wait: a wait shorter than the time left on that
	// tick would otherwise not be noticed until it landed.
	next, clock := m.armClock()
	return next, clock
}

func (m RunWatchFlowModel) expireCompletedExecutions(state RunWatchState) RunWatchState {
	if m.complete == nil {
		m.complete = make(map[string]bool)
	}
	for wi := range state.Workflows {
		for ji := range state.Workflows[wi].Jobs {
			job := &state.Workflows[wi].Jobs[ji]
			steps := job.Steps[:0]
			for _, step := range job.Steps {
				if !step.Done || job.RunsTotal <= 1 || IsFailedStepStatus(step.Status) {
					steps = append(steps, step)
					continue
				}
				key := fmt.Sprintf("%s/%d", job.ID, step.Execution)
				if m.complete[key] {
					continue
				}
				m.complete[key] = true
				if !m.loaded {
					continue
				}
				steps = append(steps, step)
			}
			job.Steps = steps
		}
	}
	return state
}

// quit stamps the elapsed time onto the result and ends the program. The view
// keeps rendering the table, so the final frame bubbletea flushes on exit is the
// finished run — the caller's summary line then prints beneath it.
func (m RunWatchFlowModel) quit() (tea.Model, tea.Cmd) {
	m.done = true
	m.result.Elapsed = m.elapsed()
	return m, tea.Quit
}

func (m RunWatchFlowModel) elapsed() time.Duration { return time.Since(m.start) }

func (m RunWatchFlowModel) timedOut() bool {
	return m.opts.Timeout > 0 && m.elapsed() >= m.opts.Timeout
}

// --- commands ---

func (m RunWatchFlowModel) cmdFetch() tea.Cmd {
	ctx, fetch := m.ctx, m.opts.Fetch
	return func() tea.Msg {
		if fetch == nil {
			return runWatchStateMsg{}
		}
		state, err := fetch(ctx)
		return runWatchStateMsg{state: state, err: err}
	}
}

// armClock schedules the next clock tick and supersedes the outstanding one (see
// clockGen). Every path that ends a tick or moves the schedule arms a new clock;
// nothing else has to reason about how many are in flight.
func (m RunWatchFlowModel) armClock() (RunWatchFlowModel, tea.Cmd) {
	m.clockGen++
	gen := m.clockGen
	return m, tea.Tick(m.clockDelay(), func(time.Time) tea.Msg { return runWatchClockMsg{gen: gen} })
}

// clockDelay is how long to wait before the next redraw: until whichever figure
// on screen changes first — the elapsed clock ticking over, or the countdown
// losing a second. A flat one-second tick drifts against both (each cycle is a
// second plus whatever the loop took), and once it is out of phase with the
// deadline it is counting down to, the figure skips a second here and lingers on
// one there. Aiming each tick at the next boundary keeps every second on screen
// for a second, and lands the last one exactly on the poll.
func (m RunWatchFlowModel) clockDelay() time.Duration {
	delay := runWatchClockTick - m.elapsed()%runWatchClockTick
	if !m.nextPoll.IsZero() {
		if untilTick := time.Until(m.nextPoll) % runWatchClockTick; untilTick > 0 && untilTick < delay {
			delay = untilTick
		}
	}
	return delay
}

// --- view ---

func (m RunWatchFlowModel) View() tea.View {
	// The final table is printed after Bubble Tea exits. Keeping this last frame
	// empty lets the inline renderer clear its bounded live view without scrolling
	// a taller completed table past the top of the terminal.
	if m.done {
		return tea.NewView("")
	}
	return components.WithWindowTitle(m.buildView(), components.FlowTitle("run watch"))
}

func (m RunWatchFlowModel) ViewTable() []string {
	var rows []string
	showJobType := hasMultipleJobTypes(m.state.Workflows)
	for _, wf := range m.state.Workflows {
		rows = append(rows, m.workflowRow(wf))
		for _, j := range wf.Jobs {
			rows = append(rows, m.jobRow(j, showJobType))
			for _, step := range j.Steps {
				rows = append(rows, m.currentStepRow(step, j.RunsTotal > 1))
				if step.Command != "" && IsFailedStepStatus(step.Status) {
					rows = append(rows, m.stepCommandRow(step))
				}
			}
		}
	}
	return rows
}

func hasMultipleJobTypes(workflows []RunWatchWorkflow) bool {
	types := make(map[string]struct{})
	for _, wf := range workflows {
		for _, job := range wf.Jobs {
			if job.Type != "" {
				types[job.Type] = struct{}{}
				if len(types) > 1 {
					return true
				}
			}
		}
	}
	return false
}

func (m RunWatchFlowModel) buildView() tea.View {
	if m.busy {
		return tea.NewView("")
	}
	if m.inMenu {
		// Keep the failed jobs in view while the step picker is open. The picker
		// replaces the live table, so project the current state down to workflows
		// that contain failed jobs and render that tree immediately above it.
		failedState := m.state
		failedState.Workflows = nil
		for _, wf := range m.state.Workflows {
			failedJobs := make([]RunWatchJob, 0, len(wf.Jobs))
			for _, job := range wf.Jobs {
				if job.Failed {
					failedJobs = append(failedJobs, job)
				}
			}
			if len(failedJobs) > 0 {
				wf.Jobs = failedJobs
				failedState.Workflows = append(failedState.Workflows, wf)
			}
		}
		failedTree := m
		failedTree.state = failedState
		var b strings.Builder
		b.WriteString(m.ViewHeader() + "\n\n")
		for _, row := range failedTree.ViewTable() {
			b.WriteString(m.line(row))
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.menu.View().Content)
		return tea.NewView(b.String())
	}
	var b strings.Builder
	b.WriteString(m.ViewHeader() + "\n\n")

	if !m.loaded {
		b.WriteString(m.line("  " + m.spinnerPrefix() + m.muted("Fetching run status…")))
		return tea.NewView(b.String())
	}

	rows := m.ViewTable()
	note := m.pendingNote()
	// Keep the frame within the viewport so the header and live footer remain
	// fixed while a large table is scrolled.
	footer := m.footer()
	// A live frame must fit the viewport to keep its header from scrolling away.
	if m.height > 0 {
		available := m.tableViewportHeight()
		if len(rows) > available {
			if available == 0 {
				rows = nil
			} else {
				window := max(1, available-1)
				maxOffset := max(0, len(rows)-window)
				offset := min(m.scroll, maxOffset)
				end := min(len(rows), offset+window)
				visible := append([]string(nil), rows[offset:end]...)
				if end < len(rows) {
					visible = append(visible, fmt.Sprintf("  … %d more rows", len(rows)-end))
				} else {
					visible = append(visible, fmt.Sprintf("  … %d rows above", offset))
				}
				rows = visible
			}
		} else {
			m.scroll = 0
		}
	}
	for _, row := range rows {
		b.WriteString(m.line(row))
	}
	if note != "" {
		b.WriteString(m.line("  " + note))
	}
	if footer != "" {
		b.WriteString("\n" + footer)
	}
	return tea.NewView(b.String())
}

// header names the run being watched: the ID, so it can be pasted into another
// command, and the branch it belongs to in the secondary color the pickers use
// for a scope. Both read their tone from the table's palette, so a themed watch
// carries the theme into its header; with a plain palette the styles render
// their text unchanged and the header is simply uncolored.
func (m RunWatchFlowModel) ViewHeader() string {
	title := m.palette.Title.Render("Watching run " + m.opts.RunID.String())
	if m.opts.Branch == "" {
		return title
	}
	return title + " " + m.palette.Secondary.Render("["+m.opts.Branch+"]")
}

// pendingNote explains a table that looks finished but is not: every workflow the
// run has produced so far has ended, and the run is still going. That is what a
// dynamic-config run looks like between its setup workflow ending and the
// continued workflow appearing — and if the continued config is rejected, the
// rows never change again, so without this line a watch that is right to keep
// waiting looks stuck on a finished table.
func (m RunWatchFlowModel) pendingNote() string {
	if m.done || m.result.State.Done || !m.state.AllWorkflowsEnded {
		return ""
	}
	return m.muted("All workflows have ended; waiting for the run itself to finish.")
}

// footer is the elapsed clock and the key hints, with a spinner leading them
// while the run is still going. The clock is followed by when the next poll
// lands, which is what makes the table legible as live — without it a run that
// has settled into the 30-second cadence looks stalled, and "r" looks like the
// only way to see anything new.
//
// Once the watch is over the whole footer goes: the keys are no longer live,
// nothing further is coming, and the summary line the caller prints underneath
// already carries the elapsed time. The final frame is the finished table alone.
func (m RunWatchFlowModel) footer() string {
	if m.done {
		return ""
	}
	if m.opts.NoFail && m.result.State.Done && m.result.State.Outcome == "failed" && m.failedWorkflowID() != uuid.Nil {
		return m.line("  " + m.muted("Press R to rerun failed jobs, f to show failed jobs, or Q to exit."))
	}
	return m.line("  "+m.spinnerPrefix()+
		m.muted("Elapsed "+FormatElapsed(m.elapsed())+" · "+m.nextPollNote())) +
		m.line("  "+components.HintsStyled(m.palette.Muted, runWatchKeys...))
}

func (m RunWatchFlowModel) failedWorkflowID() uuid.UUID {
	for _, wf := range m.result.State.Workflows {
		if wf.Status == "failed" {
			return wf.ID
		}
	}
	return uuid.Nil
}

// nextPollNote says when the run's status is next checked: a meter filling
// towards the next poll, or that one is happening right now. The meter carries
// the wait at a glance and the figure beside it makes the wait exact — the poll
// interval widens as the run goes on, so a bar alone would say "nearly there"
// without ever saying how long that is.
func (m RunWatchFlowModel) nextPollNote() string {
	remaining := ceilSeconds(time.Until(m.nextPoll))
	if m.polling || m.nextPoll.IsZero() || remaining <= 0 {
		return "updating…"
	}
	return fmt.Sprintf("next update %s %s", m.pollMeter(remaining), FormatElapsed(remaining))
}

// ceilSeconds rounds a countdown up to the next whole second. Rounding to the
// nearest would drop the figure half a second early and finish the count with a
// second still to run — which reads as the timer skipping and the run refreshing
// ahead of its own countdown. Rounded up, "1s" means "up to a second left" and
// the count reaches zero exactly when the poll fires.
func ceilSeconds(d time.Duration) time.Duration {
	// Truncate rounds towards zero, so rounding a negative duration up would send
	// it away from zero — a poll that is already overdue would read as a second
	// still to run rather than as an update in progress.
	if d <= 0 {
		return 0
	}
	if truncated := d.Truncate(time.Second); truncated != d {
		return truncated + time.Second
	}
	return d
}

// pollMeter draws the wait for the next poll as a small bar that fills as it
// elapses. Solid and outline cells carry the reading on their own, so it survives
// with color off, and both stay outside the status-glyph vocabulary (see
// statusIconStyle) — a filling circle would read as a queued or running job,
// which is exactly what the rows above the footer use ○ and ● for.
func (m RunWatchFlowModel) pollMeter(remaining time.Duration) string {
	filled := 0
	if m.wait > 0 {
		// Rounded to the nearest cell rather than truncated, so the meter tracks
		// the wait evenly instead of trailing it by up to a cell.
		elapsed := m.wait - remaining
		filled = int((int64(runWatchMeterCells)*int64(elapsed) + int64(m.wait)/2) / int64(m.wait))
	}
	filled = min(max(filled, 0), runWatchMeterCells)
	return strings.Repeat(runWatchMeterFull, filled) +
		strings.Repeat(runWatchMeterEmpty, runWatchMeterCells-filled)
}

// workflowRow renders a workflow's name, status and timing. The name links to
// the workflow in the CircleCI app and the trailing column is how long it has
// run and the clock window it ran in — live for a workflow still going, since
// the flow redraws every second.
func (m RunWatchFlowModel) workflowRow(wf RunWatchWorkflow) string {
	return fmt.Sprintf("  %s  %s %s %s",
		pad(m.link(wf.Name, wf.URL), runWatchWorkflowNameCol),
		m.palette.StatusText(wf.Symbol, wf.Symbol),
		m.statusWord(wf.Symbol, wf.Status),
		m.timing(&wf.CreatedAt, wf.EndedAt))
}

// jobRow renders one job, indented under its workflow, with the same link and
// timing columns as its workflow's row.
func (m RunWatchFlowModel) jobRow(j RunWatchJob, showType bool) string {
	parallelRuns := ""
	if j.RunsTotal > 1 {
		parallelRuns = fmt.Sprintf(" %d/%d", j.RunsDone, j.RunsTotal)
	}
	statusText := m.palette.StatusText(j.Symbol, j.Status) + parallelRuns
	if width := ansi.StringWidth(statusText); width < runWatchStatusCol {
		statusText += strings.Repeat(" ", runWatchStatusCol-width-1)
	}
	row := fmt.Sprintf("    %s  %s %s",
		pad(m.link(j.Name, j.URL), runWatchJobNameCol),
		m.palette.StatusText(j.Symbol, j.Symbol),
		statusText)
	if showType {
		row += "  " + j.Type
	}
	return row + "  " + m.timing(j.StartedAt, j.EndedAt)
}

func (m RunWatchFlowModel) currentStepRow(step RunWatchStep, parallel bool) string {
	label := fmt.Sprintf("%s %s", fmt.Sprintf("#%d", step.StepNum), step.Name)
	if parallel {
		execution := m.palette.Accent.Render(fmt.Sprintf("%d", step.Execution))
		label = fmt.Sprintf("%s %s", execution, label)
	}
	symbol := step.Symbol
	if symbol == "" {
		symbol = "●"
	}
	row := fmt.Sprintf("      %s  %s %s %s",
		pad(m.link(label, step.URL), runWatchStepNameCol),
		m.palette.StatusText(symbol, symbol),
		m.statusWord(symbol, step.Status),
		m.timing(step.StartedAt, step.EndedAt))
	return row
}
func (m RunWatchFlowModel) stepCommandRow(step RunWatchStep) string {
	command := strings.TrimPrefix(step.Command, "#!/bin/bash -eo pipefail")
	command = strings.TrimSpace(command)
	command = strings.ReplaceAll(command, "\r\n", "\n")
	command = strings.ReplaceAll(command, "\n", "\n        ")
	return "        " + m.palette.Muted.Render(command)
}

// link renders a row's name as a clickable OSC-8 hyperlink to its page in the
// CircleCI app — the watch table's answer to the link the older watcher drew on
// every workflow and job name. Links are emitted only when the palette draws
// escapes at all — which is also false with no color and under the plain themes
// — and only when the caller resolved an app URL: a terminal that asked for no
// escapes (NO_COLOR, no TTY, TERM=dumb) gets the plain name, which still names
// the row.
func (m RunWatchFlowModel) link(text, url string) string {
	if m.palette.Plain || url == "" {
		return text
	}
	return lipgloss.NewStyle().Hyperlink(url).Render(text)
}

// timing is the trailing column of a row: how long the workflow or job has run
// and the clock window it ran in ("1m2s  @ 12:00:00 → 12:01:02"). A row that has
// not started has no timing yet, so the column is absent — a queued job shows
// nothing where a running one shows its elapsed time. The duration of a row
// still going is measured against the current clock, so it grows with the
// footer's elapsed figure rather than freezing at whatever the last poll said.
func (m RunWatchFlowModel) timing(start *time.Time, end *time.Time) string {
	if start == nil || start.IsZero() {
		return ""
	}
	d := time.Since(*start)
	if end != nil {
		d = end.Sub(*start)
	}
	layout := m.clockLayout()
	clock := start.Local().Format(layout)
	if end != nil {
		clock += " → " + end.Local().Format(layout)
	}
	return m.palette.Mute(m.palette.Warning).Render(FormatElapsed(d)) +
		m.muted("  @ "+clock)
}

// clockLayout is the layout the timing column renders timestamps with: the
// caller's locale-aware layout when it supplied one, else the plain 24-hour
// default. Both hours are rendered with it, so a row's window reads the same on
// either side of the arrow.
func (m RunWatchFlowModel) clockLayout() string {
	if m.opts.ClockLayout != "" {
		return m.opts.ClockLayout
	}
	return runWatchClockLayout
}

// statusWord renders a status word in the same color as its glyph, so the two
// read as one unit — the watch table's answer to the inverse-video badge the
// older watcher drew, without the block of background that made a finished row
// shout as loudly as a running one. The word is padded before it is colored, so
// it occupies exactly the same cells with or without color and the columns stay
// aligned. An unknown symbol — and every symbol under a plain palette — leaves
// the word plain, as the glyph does.
func (m RunWatchFlowModel) statusWord(symbol, word string) string {
	return m.palette.StatusText(symbol, pad(word, runWatchStatusCol))
}

// line trims a row's trailing padding, fits it to the terminal width, and
// terminates it. Truncation is ANSI-aware, so a row cut short at the right edge
// keeps its styling intact rather than leaking an escape sequence.
func (m RunWatchFlowModel) line(row string) string {
	row = strings.TrimRight(row, " ")
	if m.width > 0 {
		row = ansi.Truncate(row, m.width, "…")
	}
	return row + "\n"
}

// spinnerPrefix is the animated spinner and its trailing space, or nothing when
// the watch is over or animation is off (CIRCLE_SPINNER_DISABLED, or a
// non-interactive session).
func (m RunWatchFlowModel) spinnerPrefix() string {
	if m.done || !m.opts.Animate {
		return ""
	}
	return m.spin.View() + " "
}

// muted de-emphasizes footer and timing text in the table's palette, leaving it
// plain when that palette draws no escapes.
func (m RunWatchFlowModel) muted(s string) string {
	return m.palette.Muted.Render(s)
}

// pad fits s to exactly w cells: padded with spaces when short, truncated with an
// ellipsis when long, so a verbose job name cannot push the status column out of
// alignment. Width is measured in cells (not bytes or runes) so a name with
// wide glyphs still lines up.
func pad(s string, w int) string {
	if width := ansi.StringWidth(s); width <= w {
		return s + strings.Repeat(" ", w-width)
	}
	return ansi.Truncate(s, w, "…")
}

// FormatElapsed renders a watch duration as the compact "1h2m3s" form, dropping
// the units that are zero from the left. It is exported because `run watch`
// prints the same elapsed figure in its final summary line, after this program
// has exited, and the two must agree.
func FormatElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

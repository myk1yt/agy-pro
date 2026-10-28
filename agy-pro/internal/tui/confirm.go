package tui

import "time"

// Confirm is the M3 y/n danger gate (delete provider, delete model,
// reset-to-default). It renders the exact warning lines the caller
// supplies and answers only y / n / Esc; every other key is consumed
// without effect, so nothing leaks past a live gate. A console-paced answer
// arriving inside the arm window (gateArmWindow) is dropped, not answered:
// the tail of a paste that opened the gate cannot close it, and no later key
// can replay the dropped answer (fail-closed).
type Confirm struct {
	title    string
	body     []string
	onAnswer func(approved bool)
	approved bool
	done     bool
	arm      gateArm
}

// NewConfirm builds a danger gate. body is the caller-composed warning
// (for example the provider and model names plus the key-values-untouched
// note); onAnswer fires once with the decision, true only on y. The gate-open
// stamp for the arm delay is taken here, so the caller must construct the
// gate when it shows it (every call site is ShowModal(NewConfirm(...))).
func NewConfirm(title string, body []string, onAnswer func(approved bool)) *Confirm {
	return &Confirm{title: title, body: body, onAnswer: onAnswer, arm: newGateArm()}
}

// Title returns the gate title.
func (c *Confirm) Title() string { return c.title }

// Body returns the warning lines exactly as supplied (never truncated or
// reworded; the caller owns the wording).
func (c *Confirm) Body(w int) []string { return c.body }

// Footer returns the gate legend. It is an English const: chrome i18n is
// decision 6B (documented English), so the gate legends get no catalog keys.
func (c *Confirm) Footer() string { return "y confirm · n/Esc cancel · other keys ignored" }

// Key answers the gate. y approves, n or Esc declines, everything else is
// swallowed; a console-paced answer inside the arm window is dropped (see
// gateArm). It returns false once the gate has an answer.
func (c *Confirm) Key(k Key, a *App) bool {
	if c.done {
		return false
	}
	approved, step := c.arm.route(k)
	if step != gateClose {
		return true
	}
	c.approved = approved
	c.done = true
	if c.onAnswer != nil {
		c.onAnswer(c.approved)
	}
	return false
}

// Approved reports the decision (meaningful after the gate closed).
func (c *Confirm) Approved() bool { return c.approved }

// Done reports whether the gate has an answer.
func (c *Confirm) Done() bool { return c.done }

// Approval is the M4 approval modal base: the ONLY path to a
// providers.json write. It renders the exact mutation summary the caller
// supplies (the providers.json diff, provider, model count before/after)
// and, like every gate, answers only y / n / Esc. A console-paced answer
// arriving inside the arm window is dropped: the tail of a paste that opened
// the modal cannot approve a registry write, and no innocent key press can
// commit the dropped approval.
type Approval struct {
	title    string
	summary  []string
	onAnswer func(approved bool)
	approved bool
	done     bool
	arm      gateArm
}

// NewApproval builds an approval modal over the exact mutation summary
// lines; onAnswer fires once with the decision, true only on y. The gate-open
// stamp for the arm delay is taken here, so the caller must construct the
// modal when it shows it (every call site is ShowModal(NewApproval(...))).
func NewApproval(title string, summary []string, onAnswer func(approved bool)) *Approval {
	return &Approval{title: title, summary: summary, onAnswer: onAnswer, arm: newGateArm()}
}

// Title returns the modal title.
func (a *Approval) Title() string { return a.title }

// Body returns the mutation summary lines exactly as supplied (byte-stable
// rendering is a test pin: the diff the user approved is the diff shown).
func (a *Approval) Body(w int) []string { return a.summary }

// Footer returns the gate legend. English const by decision 6B, like the
// Confirm legend; it now states that every non-listed key is ignored.
func (a *Approval) Footer() string { return "y approve write · n/Esc cancel · other keys ignored" }

// Key answers the modal: y approves, n or Esc declines, everything else is
// swallowed; a console-paced answer inside the arm window is dropped (see
// gateArm). It returns false once the modal closed.
func (a *Approval) Key(k Key, app *App) bool {
	if a.done {
		return false
	}
	approved, step := a.arm.route(k)
	if step != gateClose {
		return true
	}
	a.approved = approved
	a.done = true
	if a.onAnswer != nil {
		a.onAnswer(a.approved)
	}
	return false
}

// confirmAnswer resolves one key to a gate decision in CONFIRM scope. It is
// the CONFIRM-scope counterpart of EditRune: EDIT resolves the shadowed keys
// to text, the gates resolve both input languages to an answer. The y/n enum
// values and both cases of the rune carrier (Caps Lock arrives as RuneKey)
// answer; Esc declines; every other key is not an answer and stays swallowed.
func confirmAnswer(k Key) (approved bool, answered bool) {
	switch k {
	case KeyYes, RuneKey('y'), RuneKey('Y'):
		return true, true
	case KeyNo, KeyEsc, RuneKey('n'), RuneKey('N'):
		return false, true
	}
	return false, false
}

// gateArmWindow is the arm delay of the two gates: an answer key delivered
// this soon after the gate opened is not a human decision. A host injects a
// clipboard paste as one key event per character with no perceptible gap, and
// a paste whose drain outlived the console reader's burst window can leak its
// tail into a gate that opened mid-drain. A console-paced answer inside this
// window is dropped, so a leaked y/n cannot close the gate while the burst is
// still arriving; a human key press against a fresh modal is an order of
// magnitude slower, so a deliberate answer is never dropped. Line-mode and
// scripted sources never mark their keys as console-paced, so their answers
// are immediate (see consoleKeySerial).
const gateArmWindow = 25 * time.Millisecond

// gateClock is the package clock seam behind the arm delay. It is the real
// clock in production and in the tests: the pins inject a clock per gate
// (gateArm.now) instead of stepping this one, so no test sleeps and no test
// depends on how long a scripted key takes to arrive.
var gateClock = time.Now

// consoleKeySerial counts the keys the raw console reader has delivered. It is
// the pacing seam of the arm delay: the console is the only source where the
// tail of a paste can leak into a live gate, so its reads advance this counter
// and every gate compares the counter with the value stamped when it opened. A
// line-mode or scripted source never advances it, so its keys leave the gate
// unpaced and its answers immediate (the pre-arm-delay behavior). The zero
// value means no console key was ever read: a source that never marks itself is
// unpaced by construction. Tests drive the paced path with markConsoleKeyRead,
// exactly as the console readers do.
var consoleKeySerial uint64

// markConsoleKeyRead records one console-paced delivery. Production reaches it
// only through consoleDelivered, so there is one place where a console key
// becomes visible to the gates.
func markConsoleKeyRead() { consoleKeySerial++ }

// consoleDelivered is the single funnel through which a console reader hands a
// key to the rest of the package: it records the delivery on the pacing seam
// and returns the key unchanged. Both console surfaces (console_windows.go and
// console_other.go) go through it, so the arm delay and the console boundary
// cannot drift apart.
func consoleDelivered(k Key, ok bool) (Key, bool) {
	if ok {
		markConsoleKeyRead()
	}
	return k, ok
}

// gateArm is the arm-delay state shared by Confirm and Approval. openedAt and
// openSerial are stamped at construction, which is when every call site shows
// the gate. suppressed records an answer the window dropped: it is audit state
// for the pins only, never a buffer, because no key ever commits it.
type gateArm struct {
	now        func() time.Time // per-gate clock seam; nil means gateClock
	openedAt   time.Time
	openSerial uint64 // consoleKeySerial when the gate opened
	suppressed bool   // an answer was dropped inside the window; record only
}

// newGateArm stamps the gate-open time and the console pacing serial.
func newGateArm() gateArm {
	a := gateArm{openSerial: consoleKeySerial}
	a.openedAt = a.clock()
	return a
}

// clock reads the per-gate seam, then the package seam.
func (a *gateArm) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return gateClock()
}

// paced reports whether the key now being routed arrived from the raw console
// reader, the one source where a paste remainder can leak into a live gate. The
// reader advances consoleKeySerial on every key it delivers, so this is true for
// each console key a gate receives after it opened, and false for every key of a
// source that never marks itself (line mode, scripted tests).
func (a *gateArm) paced() bool { return consoleKeySerial != a.openSerial }

// gateStep is what routing one key through the arm delay decided.
type gateStep int

const (
	gateSwallow gateStep = iota // key consumed, the gate stays open
	gateClose                   // key resolved the gate: fire the answer once
)

// route applies the arm delay to one key and reports the decision when the key
// resolves the gate. A console-paced y/Y/n/N inside the window is dropped and
// recorded, never answered: a human cannot answer that fast, a leaked paste
// remainder can. Every non-answer key is swallowed exactly as before and also
// clears the record, so an innocent key press (a digit, Enter, an arrow) can
// never commit a dropped answer; that fail-open flush was the confirmed defect.
// A dropped answer is not a buffer: no key ever replays it. The only way to
// close a gate is a fresh answer key, which decides for itself. A second answer
// inside the window is no more human than the first, so it is dropped as well,
// and the deliberate answer lands as soon as the window has elapsed. Esc is
// never dropped, because canceling must not wait.
func (a *gateArm) route(k Key) (bool, gateStep) {
	approved, answered := confirmAnswer(k)
	a.suppressed = false
	if !answered {
		return false, gateSwallow
	}
	if a.paced() && k != KeyEsc && a.clock().Sub(a.openedAt) < gateArmWindow {
		a.suppressed = true
		return false, gateSwallow
	}
	return approved, gateClose
}

// Approved reports the decision (meaningful after the modal closed).
func (a *Approval) Approved() bool { return a.approved }

// Done reports whether the modal has an answer.
func (a *Approval) Done() bool { return a.done }

// Choice is the M2 numbered choice overlay. It is built on the shared
// stepList, so both input languages work here by construction: digits
// select+activate an option directly, arrows move a visible cursor and
// Enter activates it. Esc cancels (the callback receives index -1).
type Choice struct {
	title  string
	list   *stepList
	onPick func(idx int, value string)
	picked bool
}

// NewChoice builds a numbered choice overlay over the options.
func NewChoice(title string, options []string, onPick func(idx int, value string)) *Choice {
	ch := &Choice{title: title, onPick: onPick}
	rows := make([]Row, len(options))
	for i, o := range options {
		rows[i] = Row{Label: o}
	}
	ch.list = newStepList(ch.pick)
	ch.list.SetRows(rows)
	return ch
}

// SetPageSize sets the choice window height (defaults to 10).
func (ch *Choice) SetPageSize(n int) { ch.list.SetPageSize(n) }

// Title returns the overlay title.
func (ch *Choice) Title() string { return ch.title }

// Body returns the visible options, numbered and cursor-marked.
func (ch *Choice) Body(w int) []string { return ch.list.Render(w) }

// Footer returns the shared two-handled legend.
func (ch *Choice) Footer() string { return ch.list.FooterHint() }

// Cursor returns the visible cursor index.
func (ch *Choice) Cursor() int { return ch.list.Cursor() }

// Key routes one key to the shared list. Esc cancels; a digit or Enter
// activation closes the overlay through the pick callback. Other keys keep
// it open.
func (ch *Choice) Key(k Key, a *App) bool {
	if k == KeyEsc {
		if !ch.picked {
			ch.picked = true
			if ch.onPick != nil {
				ch.onPick(-1, "")
			}
		}
		return false
	}
	ch.list.HandleKey(k)
	return !ch.picked
}

func (ch *Choice) pick(idx int) {
	if ch.picked {
		return
	}
	ch.picked = true
	if ch.onPick != nil {
		ch.onPick(idx, ch.list.Row(idx).Label)
	}
}

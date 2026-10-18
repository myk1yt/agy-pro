package tui

// Confirm is the M3 y/n danger gate (delete provider, delete model,
// reset-to-default). It renders the exact warning lines the caller
// supplies and answers only y / n / Esc; every other key is consumed
// without effect, so nothing leaks past a live gate.
type Confirm struct {
	title    string
	body     []string
	onAnswer func(approved bool)
	approved bool
	done     bool
}

// NewConfirm builds a danger gate. body is the caller-composed warning
// (for example the provider and model names plus the key-values-untouched
// note); onAnswer fires once with the decision, true only on y.
func NewConfirm(title string, body []string, onAnswer func(approved bool)) *Confirm {
	return &Confirm{title: title, body: body, onAnswer: onAnswer}
}

// Title returns the gate title.
func (c *Confirm) Title() string { return c.title }

// Body returns the warning lines exactly as supplied (never truncated or
// reworded; the caller owns the wording).
func (c *Confirm) Body(w int) []string { return c.body }

// Footer returns the gate legend.
func (c *Confirm) Footer() string { return "y confirm · n/Esc cancel" }

// Key answers the gate. y approves, n or Esc declines, everything else is
// swallowed. It returns false once the gate has an answer.
func (c *Confirm) Key(k Key, a *App) bool {
	if c.done {
		return false
	}
	approved, answered := confirmAnswer(k)
	if !answered {
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
// and, like every gate, answers only y / n / Esc.
type Approval struct {
	title    string
	summary  []string
	onAnswer func(approved bool)
	approved bool
	done     bool
}

// NewApproval builds an approval modal over the exact mutation summary
// lines; onAnswer fires once with the decision, true only on y.
func NewApproval(title string, summary []string, onAnswer func(approved bool)) *Approval {
	return &Approval{title: title, summary: summary, onAnswer: onAnswer}
}

// Title returns the modal title.
func (a *Approval) Title() string { return a.title }

// Body returns the mutation summary lines exactly as supplied (byte-stable
// rendering is a test pin: the diff the user approved is the diff shown).
func (a *Approval) Body(w int) []string { return a.summary }

// Footer returns the gate legend.
func (a *Approval) Footer() string { return "y approve write · n/Esc cancel" }

// Key answers the modal: y approves, n or Esc declines, everything else is
// swallowed. It returns false once the modal closed.
func (a *Approval) Key(k Key, app *App) bool {
	if a.done {
		return false
	}
	approved, answered := confirmAnswer(k)
	if !answered {
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

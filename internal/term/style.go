package term

// ANSI control sequences. Only style.go may use them, so command handlers
// never embed raw escapes.
const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiFaint = "\x1b[2m"

	ansiWhite = "\x1b[37m"
	ansiGray  = "\x1b[90m"
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
	ansiAmber = "\x1b[33m"
	ansiCyan  = "\x1b[36m"
)

func (s Style) paint(code, text string) string {
	if !s.Enabled {
		return text
	}
	return code + text + ansiReset
}

// Bold renders primary text (IDs, headings) in bright bold white.
func (s Style) Bold(text string) string {
	return s.paint(ansiBold+ansiWhite, text)
}

// Brand renders the ABOUEI wordmark in bold white.
func (s Style) Brand(text string) string {
	return s.paint(ansiBold+ansiWhite, text)
}

// Muted renders secondary metadata in dark gray.
func (s Style) Muted(text string) string {
	return s.paint(ansiGray, text)
}

// Faint renders the least important hints.
func (s Style) Faint(text string) string {
	return s.paint(ansiFaint+ansiGray, text)
}

// Success renders healthy/recovered states in restrained green.
func (s Style) Success(text string) string {
	return s.paint(ansiGreen, text)
}

// Danger renders down/open/error states in controlled red.
func (s Style) Danger(text string) string {
	return s.paint(ansiRed, text)
}

// Warning renders unknown/degraded/canceled states in amber.
func (s Style) Warning(text string) string {
	return s.paint(ansiAmber, text)
}

// Accent renders the single brand accent (left rule, incident markers)
// in muted cyan. Use sparingly.
func (s Style) Accent(text string) string {
	return s.paint(ansiCyan, text)
}

// Rule returns a thin muted separator sized to the terminal width,
// capped for readability. Plain mode returns a short ASCII rule.
func (s Style) Rule() string {
	n := s.Width
	if n > 56 {
		n = 56
	}
	if n < 16 {
		n = 16
	}
	bar := ""
	for i := 0; i < n; i++ {
		bar += "-"
	}
	if !s.Enabled {
		return bar
	}
	// Use a thin box rule only when color is on; it is decorative and the
	// plain fallback above keeps pipes and tests ASCII-clean.
	thin := ""
	for i := 0; i < n; i++ {
		thin += "-"
	}
	return s.Muted(thin)
}

// Status colors a domain status word while preserving its exact text so
// greps and tests for "healthy", "down", "unknown" keep matching in plain
// mode and remain readable in color mode. No new states are invented here:
// unknown words fall back to muted text.
func (s Style) Status(word string) string {
	switch word {
	case "healthy", "recovered", "success", "valid":
		return s.Success(word)
	case "down", "unhealthy", "error", "open", "still_open", "failed":
		return s.Danger(word)
	case "unknown", "orphaned", "canceled", "degraded", "warning", "pending":
		return s.Warning(word)
	case "none", "closed", "":
		return s.Muted(word)
	default:
		return s.Muted(word)
	}
}

// Label styles short semantic badges (HEALTHY, DOWN, ...) used in the
// design spec. It maps only real domain outcomes; callers must not invent
// badges for states the engine never produces.
func (s Style) Label(word string) string {
	switch word {
	case "HEALTHY", "RECOVERED":
		return s.Success(word)
	case "DOWN", "ERROR", "OPEN":
		return s.Danger(word)
	case "WARNING", "UNKNOWN", "DEGRADED", "CANCELED":
		return s.Warning(word)
	default:
		return s.Muted(word)
	}
}

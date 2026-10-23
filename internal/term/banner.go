package term

import (
	"fmt"
	"strings"
)

// WordmarkMark is the terminal fallback glyph shown beside ABOUEI. It is a
// deliberate text wordmark, not the logo itself: the genuine logo PNG is
// preserved under assets/brand and referenced by LogoAsset. The bar keeps
// the composition recognizable at one line without pretending to be artwork.
func (s Style) mark() string {
	if s.Enabled {
		return s.Accent("|")
	}
	return "|"
}

// Wordmark returns the one-line brand lockup: [mark] ABOUEI.
func (s Style) Wordmark() string {
	return s.mark() + " " + s.Brand(BrandName)
}

// Banner returns the compact branded startup presentation:
//
//	| ABOUEI
//	ABOUEICHECKER - Backend Health Monitoring
//
// The logo's aspect ratio is never distorted because no image is stretched
// into the terminal; the PNG stays untouched in assets/brand. The banner is
// intentionally 2-3 lines so it never dominates the screen, and it collapses
// to one line on narrow terminals.
func (s Style) Banner() string {
	if s.Narrow() {
		return s.Wordmark() + s.Muted(" · "+ProductName)
	}
	var b strings.Builder
	b.WriteString(s.Wordmark())
	b.WriteString("\n")
	b.WriteString(s.Bold(ProductName))
	b.WriteString(s.Muted("  ·  " + Descriptor))
	return b.String()
}

// VersionBanner adds the binary version under the product line for the
// version command's human-readable presentation.
func (s Style) VersionBanner(version string) string {
	base := s.Banner()
	if version == "" {
		return base
	}
	return base + "\n" + s.Muted("version "+version)
}

// HelpHeader is prepended to Cobra help output for human-readable help.
func (s Style) HelpHeader() string {
	return s.Banner() + "\n" + s.Rule()
}

// ErrorLine formats the single stderr error line. It keeps the exact
// "Error: <message>" shape so tooling and tests keep matching.
func (s Style) ErrorLine(message string) string {
	if message == "" {
		message = "internal error"
	}
	return s.Danger("Error:") + " " + message
}

// SummaryLine styles the "Summary:" lead while keeping the word intact.
func (s Style) SummaryLine(rest string) string {
	return s.Bold("Summary:") + " " + rest
}

// SectionTitle styles section headings such as "Observations:".
func (s Style) SectionTitle(title string) string {
	return s.Bold(title)
}

// FormatBackendLine renders one aligned "id: status (...)" row. StatusWord
// is colored; id is bold; detail stays plain/muted so long URLs and errors
// truncate safely without breaking alignment.
func (s Style) FormatBackendLine(id, statusWord, detail string) string {
	head := s.Bold(s.Fit(id, 48)) + ": " + s.Status(statusWord)
	if detail == "" {
		return head
	}
	return head + " " + s.Muted("("+s.Fit(detail, s.detailBudget())+")")
}

func (s Style) detailBudget() int {
	budget := s.Width - 24
	if budget < 24 {
		budget = 24
	}
	if budget > 160 {
		budget = 160
	}
	return budget
}

// IncidentTag renders "[incident open]" style tags with a restrained accent.
func (s Style) IncidentTag(status string) string {
	if status == "" || status == "none" {
		return ""
	}
	return " " + s.Accent("[incident "+s.Fit(status, 24)+"]")
}

// VersionLineFallback formats the historical version line. Kept here so the
// CLI and tests share one shape; styling never splits the inner words.
func VersionLineFallback(name, version, commit, date, goVersion, platform string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s, %s %s)",
		name, version, commit, date, goVersion, platform)
}

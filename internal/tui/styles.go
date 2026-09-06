package tui

import (
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Rake-Pro/mailshear/internal/headers"
)

// palette resolves the semantic colors once, for the terminal's background.
// Everything downstream refers to a role (ok, warn, accent) and never to a
// number, so the whole scheme moves from one place and the UI still reads
// correctly with NO_COLOR set or in a 16-color terminal.
type palette struct {
	Accent   color.Color
	Text     color.Color
	Muted    color.Color
	Faint    color.Color
	OK       color.Color
	Warn     color.Color
	Err      color.Color
	Info     color.Color
	Border   color.Color
	AccentBG color.Color
}

func newPalette(dark bool) palette {
	lightDark := lipgloss.LightDark(dark)
	col := func(light, dark string) color.Color {
		return lightDark(lipgloss.Color(light), lipgloss.Color(dark))
	}
	return palette{
		Accent:   col("62", "111"),  // indigo: headers, focus, selection
		Text:     col("235", "252"), // body text
		Muted:    col("243", "245"), // metadata
		Faint:    col("250", "240"), // hidden, protected, disabled
		OK:       col("28", "78"),   // success, one-click
		Warn:     col("130", "214"), // warnings, probable, plain http
		Err:      col("160", "203"), // failures, refusals
		Info:     col("25", "75"),   // links, manual follow-up, mailto
		Border:   col("250", "240"),
		AccentBG: col("189", "60"), // selection: an accent wash, not reverse
	}
}

// styles are the rendered tokens. Panels are rounded and padded one cell on
// each side; nothing sets Height, so content is padded to the inner height
// instead (see panel).
type styles struct {
	p palette

	title    lipgloss.Style
	subtle   lipgloss.Style
	chip     lipgloss.Style
	status   lipgloss.Style
	notice   lipgloss.Style
	prompt   lipgloss.Style
	detail   lipgloss.Style
	help     lipgloss.Style
	header   lipgloss.Style
	selected lipgloss.Style
	dim      lipgloss.Style
	ok       lipgloss.Style
	warn     lipgloss.Style
	err      lipgloss.Style
	info     lipgloss.Style
	accent   lipgloss.Style
	bold     lipgloss.Style

	box        lipgloss.Style
	boxWarn    lipgloss.Style
	boxErr     lipgloss.Style
	boxFocus   lipgloss.Style
	panelTitle lipgloss.Style

	barFull  lipgloss.Style
	barEmpty lipgloss.Style
}

func newStyles() styles { return newStylesFor(darkBackground()) }

// darkBackground asks the terminal once, at startup, and falls back to dark
// (the common case, and the safer guess for a light-on-dark palette).
func darkBackground() bool {
	if v := os.Getenv("COLORFGBG"); v != "" {
		// "15;0" means light text on a dark background.
		if parts := strings.Split(v, ";"); len(parts) > 1 {
			switch parts[len(parts)-1] {
			case "7", "15":
				return false
			}
		}
	}
	return lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
}

func newStylesFor(dark bool) styles {
	p := newPalette(dark)
	base := lipgloss.NewStyle()
	panel := base.
		Border(lipgloss.RoundedBorder()).
		BorderForeground(p.Border).
		Padding(0, 1)

	return styles{
		p:        p,
		title:    base.Bold(true).Foreground(p.Accent),
		subtle:   base.Foreground(p.Muted),
		chip:     base.Foreground(p.Accent),
		status:   base.Foreground(p.Text).Bold(true),
		notice:   base.Foreground(p.Warn),
		prompt:   base.Foreground(p.Info),
		detail:   base.Foreground(p.Muted),
		help:     base.Foreground(p.Faint),
		header:   base.Bold(true).Foreground(p.Muted),
		selected: base.Background(p.AccentBG).Foreground(p.Text).Bold(true),
		dim:      base.Foreground(p.Faint),
		ok:       base.Foreground(p.OK),
		warn:     base.Foreground(p.Warn),
		err:      base.Foreground(p.Err),
		info:     base.Foreground(p.Info),
		accent:   base.Foreground(p.Accent),
		bold:     base.Bold(true),

		box:        panel,
		boxWarn:    panel.BorderForeground(p.Warn),
		boxErr:     panel.BorderForeground(p.Err),
		boxFocus:   panel.BorderForeground(p.Accent),
		panelTitle: base.Bold(true).Foreground(p.Accent),

		barFull:  base.Foreground(p.Accent),
		barEmpty: base.Foreground(p.Faint),
	}
}

// method colors the unsubscribe method so the automatable ones stand out from
// the ones that will need a browser.
func (s styles) method(m string) string {
	switch m {
	case "oneclick":
		return s.ok.Render(m)
	case "http":
		return s.warn.Render(m)
	case "mailto":
		return s.info.Render(m)
	case "", "none":
		return s.dim.Render("none")
	default:
		return m
	}
}

// unsubStatus colors an unsubscribe outcome: green done, yellow probably,
// blue needs you, red failed.
func (s styles) unsubStatus(status string) string {
	switch status {
	case "ok":
		return s.ok.Render(status)
	case "probable":
		return s.warn.Render(status)
	case "manual":
		return s.info.Render(status)
	case "failed":
		return s.err.Render(status)
	default:
		return s.dim.Render(status)
	}
}

// panel renders a bordered box of an exact outer width with its content
// padded to exactly innerH lines. Height is never set on the style: the
// border adds its own two rows on top of what is returned here.
func (s styles) panel(style lipgloss.Style, title string, lines []string, outerW, innerH int) string {
	innerW := outerW - 4 // two border columns, two padding columns
	if innerW < 1 {
		innerW = 1
	}
	body := make([]string, 0, innerH+1)
	if title != "" {
		body = append(body, s.panelTitle.Render(truncate(title, innerW)))
	}
	for _, l := range lines {
		body = append(body, truncate(l, innerW))
	}
	if len(body) > innerH {
		body = body[:innerH]
	}
	for len(body) < innerH {
		body = append(body, "")
	}
	// Lipgloss v2 counts the border and the padding inside Width, so this is
	// the outer width, not the content width.
	return style.Width(outerW).Render(strings.Join(body, "\n"))
}

// bar draws a determinate progress bar of exactly width cells. It is drawn
// with block characters rather than an animated component so a frame is
// always a pure function of the counters behind it.
func (s styles) bar(width int, frac float64) string {
	if width < 3 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	full := int(frac * float64(width))
	if full > width {
		full = width
	}
	return s.barFull.Render(strings.Repeat("=", full)) +
		s.barEmpty.Render(strings.Repeat("-", width-full))
}

// cell is the last line of defence before a string that came from outside
// this program reaches the terminal: mail headers, audit logs, mailbox names,
// provider error text. Escape sequences in any of them would otherwise be
// executed rather than shown, so nothing user-supplied is rendered without
// passing through here. Styled strings must not be: the escape codes lipgloss
// produces are exactly what this removes.
func cell(str string) string { return headers.Clean(str) }

// cells is cell over a slice, for the list-valued fields.
func cells(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, cell(s))
	}
	return out
}

// truncate cuts a string to a display width. ansi.Truncate counts cells and
// skips escape codes, so it is safe on already-styled strings.
func truncate(str string, maxWidth int) string {
	if maxWidth < 1 {
		return ""
	}
	return ansi.Truncate(str, maxWidth, "...")
}

// pad right-pads to an exact display width, measuring cells rather than
// bytes so styled strings still line up in a column.
func pad(str string, width int) string {
	str = truncate(str, width)
	if n := width - lipgloss.Width(str); n > 0 {
		return str + strings.Repeat(" ", n)
	}
	return str
}

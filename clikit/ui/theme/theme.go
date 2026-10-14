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

package theme

import "charm.land/lipgloss/v2"

// Semantic color tokens. Use these instead of raw lipgloss.Color values so
// every widget speaks the same visual language, and a future palette swap
// only has to touch this file.
//
// 256-color codes are used (not hex) so terminals with reduced color depth
// degrade gracefully instead of falling back to white.
var (
	ColorAccent    = lipgloss.Color("205") // pink — focused input
	ColorSecondary = lipgloss.Color("179") // gold — secondary labels (e.g. scope in a title)
	ColorRunning   = lipgloss.Color("39")  // blue — in-progress state
	ColorSuccess   = lipgloss.Color("42")  // green — completed actions
	ColorError     = lipgloss.Color("196") // red — failures
	ColorWarning   = lipgloss.Color("220") // yellow — warnings, "press Y to confirm"
	ColorMuted     = lipgloss.Color("244") // gray — footer hints, less important text
)

// Style presets that combine the colors above with shared layout choices
// (bold/margins/etc). Widgets should reach for these before declaring a
// local style; only fork into a one-off style when the preset genuinely
// doesn't fit.
var (
	// TitleStyle is for prompt headings and emphasized labels. Bold, no color
	// so it works across light and dark terminals.
	TitleStyle = lipgloss.NewStyle().Bold(true)

	// AccentStyle is for active or in-progress UI: the spinner glyph, the
	// focused-input cursor accent, anything the user's eye should track.
	AccentStyle = lipgloss.NewStyle().Foreground(ColorAccent)

	// SecondaryStyle is for a secondary label that should read as distinct from
	// the primary accent without competing with it — e.g. the active scope
	// "(main branch)" appended to a picker title.
	SecondaryStyle = lipgloss.NewStyle().Foreground(ColorSecondary)

	// RunningStyle marks in-progress state (e.g. a running job's status glyph),
	// distinct from the pink accent used for focus/spinner.
	RunningStyle = lipgloss.NewStyle().Foreground(ColorRunning)

	// HelperStyle is for de-emphasized hint text — footers like
	// "(esc to quit)", inline shortcuts, secondary instructions.
	HelperStyle = lipgloss.NewStyle().Foreground(ColorMuted)

	// SuccessStyle / ErrorStyle / WarningStyle are for status glyphs and
	// short status words. Pair them with the icons below.
	SuccessStyle = lipgloss.NewStyle().Foreground(ColorSuccess)
	ErrorStyle   = lipgloss.NewStyle().Foreground(ColorError)
	WarningStyle = lipgloss.NewStyle().Foreground(ColorWarning)

	NoColorStyle = lipgloss.NewStyle().Foreground(lipgloss.NoColor{})

	// SearchMatchStyle highlights every pager search hit; SearchSelectedStyle
	// marks the one the viewport is currently scrolled to (like the focused
	// match in less). Dark text on a colored background keeps hits legible
	// regardless of the underlying syntax color.
	SearchMatchStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("232")).Background(ColorWarning)
	SearchSelectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("232")).Background(ColorAccent)
)

// SeriesStyles are the colors for telling apart data series overlaid on one
// chart, in the order they should be handed out. They are the semantic tokens
// above, reused for a purpose that has no semantics of its own — the only thing
// that matters is that adjacent entries are easy to tell apart, including for the
// most common forms of color blindness, which is why the blue/green/pink run
// leads and red comes last.
//
// There are deliberately only five. Beyond that, colors stop being reliably
// distinguishable and a caller is better off drawing separate charts than
// stacking more lines onto one.
var SeriesStyles = []lipgloss.Style{
	RunningStyle,   // blue
	SuccessStyle,   // green
	AccentStyle,    // pink
	SecondaryStyle, // gold
	ErrorStyle,     // red
}

// SeriesStyle returns the style for series i, cycling once the palette is
// exhausted so an out-of-range index still renders rather than panicking. Two
// series sharing a color is a presentation compromise; a crash is not.
func SeriesStyle(i int) lipgloss.Style {
	if i < 0 {
		i = 0
	}
	return SeriesStyles[i%len(SeriesStyles)]
}

// Palette is one color theme's set of semantic styles. The tokens above are the
// house palette; a Palette lets a view draw in whichever theme the user picked
// with --theme or `circleci setting set theme`, instead of always drawing in the
// house colors.
//
// Status colors keep their meaning in every palette — green succeeds, red fails,
// yellow warns, blue is in progress — because color here carries meaning and not
// decoration (see agents/04-output.md). What a theme changes is the accent,
// secondary and muted tones around them, which have no semantics of their own.
type Palette struct {
	Title     lipgloss.Style
	Accent    lipgloss.Style
	Secondary lipgloss.Style
	Running   lipgloss.Style
	Success   lipgloss.Style
	Error     lipgloss.Style
	Warning   lipgloss.Style
	Muted     lipgloss.Style

	// Plain is true for a palette that draws no escapes at all — the "ascii" and
	// "notty" themes. A view that would otherwise emit a non-color escape (an
	// OSC-8 hyperlink, say) must consult this as well as whether color is on.
	Plain bool
}

// StatusStyle maps a symbol from apiclient.PhaseOutcomeSymbol to this palette's
// style for it, reporting false for a symbol that should stay uncolored. It is
// the one table every view colors a status glyph through, so a ✓ is green and a
// ✗ red wherever they appear, in whichever theme.
func (p Palette) StatusStyle(symbol string) (lipgloss.Style, bool) {
	switch symbol {
	case "✓":
		return p.Success, true // succeeded
	case "✗":
		return p.Error, true // failed
	case "!":
		return p.Warning, true // errored / timed out
	case "●":
		return p.Running, true // running
	case "○", "⊘", "•":
		// Created/queued, canceled, and the neutral bullet PhaseOutcomeSymbol falls
		// back to for a phase or outcome it does not know. The bullet is muted for the
		// same reason: uncolored, it reads as a filled dot much like the running one.
		return p.Muted, true
	default:
		return lipgloss.Style{}, false
	}
}

// StatusText renders text in the color this palette gives symbol, leaving it
// untouched when the symbol has no color of its own or the palette draws none.
// It is the single coloring step for anything that spells out a status, so a
// view's glyph and its status word come out the same color by construction
// rather than by two call sites agreeing.
func (p Palette) StatusText(symbol, text string) string {
	if style, ok := p.StatusStyle(symbol); ok {
		return style.Render(text)
	}
	return text
}

// PlainPalette is the palette that draws no escapes at all: every style renders
// its text unchanged, the title's bold included. It is what a view uses when
// color is off entirely (no TTY, NO_COLOR, TERM=dumb) and the palette for the
// "ascii" and "notty" themes, so a view has one code path whether it is
// uncolored by its environment or by the theme the user chose.
func PlainPalette() Palette { return plainPalette }

// DefaultPalette is the house palette: the semantic tokens above, assembled into
// one value. It is what a view draws in when no theme was resolved, and the
// palette for the themes that have no colors of their own to impose ("dark" and
// "light", which describe the background rather than a scheme) — its 256-color
// codes were chosen to read on either background, so it does not need a light
// variant. "ansi" has a palette of its own; see ansiPalette.
func DefaultPalette() Palette {
	return Palette{
		Title:     TitleStyle,
		Accent:    AccentStyle,
		Secondary: SecondaryStyle,
		Running:   RunningStyle,
		Success:   SuccessStyle,
		Error:     ErrorStyle,
		Warning:   WarningStyle,
		Muted:     HelperStyle,
	}
}
func DefaultPaletteFor(color bool) Palette {
	if color {
		return DefaultPalette()
	}
	return PlainPalette()
}

// PaletteFor returns the palette for a resolved color theme name. The names are
// the ones iostream.ValidThemes accepts, which is why this mapping lives here
// next to the tokens rather than in the package that validates them: the palette
// is the thing the name selects.
//
// "auto" never reaches this function in practice — iostream resolves it to
// "dark" or "light" against the terminal background first — and anything
// unrecognized falls back to the house palette rather than to no styling.
func PaletteFor(name string) Palette {
	switch name {
	case "ansi":
		return ansiPalette
	case "dracula":
		return draculaPalette
	case "tokyo-night":
		return tokyoNightPalette
	case "pink":
		return pinkPalette
	case "ascii", "notty":
		return plainPalette
	default:
		return DefaultPalette()
	}
}

// The branded palettes below give each theme the tones it is known for, taken
// from the colors its matching markdown style uses, so a view and the markdown
// around it agree on what the theme looks like. The status colors stay semantic:
// every theme keeps a green that reads as success, a red that reads as failure,
// and so on, rather than adopting the theme's signature color for an outcome it
// does not mean.
//
// Every code is a 256-color index for the same reason the tokens above are —
// reduced-depth terminals degrade gracefully instead of falling back to white —
// with the source color named alongside so the choice is checkable rather than
// magic.
var (
	// ansiPalette is the "ansi" theme: only the standard 16 indices, so it defers
	// to whatever colors the user's terminal is configured with — the same choice
	// the matching markdown style makes. Accent and secondary are that style's own
	// link and heading colors.
	ansiPalette = brandedPalette(
		"6",  // accent — cyan, the style's link color
		"12", // secondary — bright blue, the style's heading color
		"4",  // running — blue
		"2",  // success — green
		"1",  // error — red
		"3",  // warning — yellow
		"8",  // muted — gray
	)

	// draculaPalette is the dracula theme (#ff79c6 pink accent, #bd93f9 heading
	// purple, #8be9fd cyan, #50fa7b green, #ff5555 red, #f1fa8c yellow, #6272a4
	// comment gray).
	draculaPalette = brandedPalette(
		"212", // accent — pink
		"141", // secondary — purple
		"117", // running — cyan
		"84",  // success — green
		"203", // error — red
		"228", // warning — yellow
		"61",  // muted — comment gray
	)

	// tokyoNightPalette is the tokyo-night theme (#bb9af7 purple accent, #7aa2f7
	// heading blue, #2ac3de cyan, #9ece6a green, #f7768e red, #ff9e64 orange,
	// #565f89 comment gray).
	tokyoNightPalette = brandedPalette(
		"141", // accent — purple
		"111", // secondary — blue
		"44",  // running — cyan
		"149", // success — green
		"210", // error — red
		"216", // warning — orange
		"60",  // muted — comment gray
	)

	// pinkPalette is glamour's pink theme (212 pink heading, 99 link).
	pinkPalette = brandedPalette(
		"212", // accent — pink
		"99",  // secondary — violet, the theme's link color
		"39",  // running — blue
		"42",  // success — green
		"196", // error — red
		"220", // warning — yellow
		"244", // muted — gray
	)

	// plainPalette is the "ascii"/"notty" palette: every style renders its text
	// unchanged, so a view that uses it emits no escape sequences at all. The
	// title drops its bold along with the colors — those themes mean plain text,
	// not merely uncolored text.
	plainPalette = Palette{
		Title:     lipgloss.NewStyle(),
		Accent:    NoColorStyle,
		Secondary: NoColorStyle,
		Running:   NoColorStyle,
		Success:   NoColorStyle,
		Error:     NoColorStyle,
		Warning:   NoColorStyle,
		Muted:     NoColorStyle,
		Plain:     true,
	}
)

// brandedPalette assembles a palette from the colors a branded theme uses, with
// a plain title shared by all of them — bold with no color reads correctly on
// any background, the same reason TitleStyle does.
func brandedPalette(accent, secondary, running, success, errColor, warning, muted string) Palette {
	fg := func(c string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c))
	}
	return Palette{
		Title:     TitleStyle,
		Accent:    fg(accent),
		Secondary: fg(secondary),
		Running:   fg(running),
		Success:   fg(success),
		Error:     fg(errColor),
		Warning:   fg(warning),
		Muted:     fg(muted),
	}
}

// Status icons. Keep these in one place so prompts, results, and JSON-text
// fallbacks all reach for the same glyph.
const (
	IconOK   = "✓"
	IconWarn = "⚠"
	IconFail = "✗"
)

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

package iostream

import (
	"os"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// clearTerminalEnv resets every environment variable that influences terminal
// detection so a test starts from a known-interactive baseline regardless of
// where it runs (notably, CI sets CI=true on the very machine running these).
func clearTerminalEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"CI", "CIRCLECI", "CIRCLE_NO_INTERACTIVE", "NO_COLOR", "CIRCLE_NO_COLOR"} {
		t.Setenv(k, "")
	}
	t.Setenv("TERM", "xterm-256color")
}

func TestColorDisabled(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "interactive defaults", env: nil, want: false},
		{name: "NO_COLOR", env: map[string]string{"NO_COLOR": "1"}, want: true},
		{name: "CIRCLE_NO_COLOR", env: map[string]string{"CIRCLE_NO_COLOR": "1"}, want: true},
		{name: "TERM=dumb outside CircleCI", env: map[string]string{"TERM": "dumb"}, want: true},
		// CircleCI sets TERM=dumb but renders ANSI, so color stays on there.
		{name: "TERM=dumb in CircleCI", env: map[string]string{"TERM": "dumb", "CIRCLECI": "true"}, want: false},
		// An explicit opt-out still wins, even in CircleCI.
		{name: "NO_COLOR wins in CircleCI", env: map[string]string{"TERM": "dumb", "CIRCLECI": "true", "NO_COLOR": "1"}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearTerminalEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			assert.Check(t, cmp.Equal(colorDisabled(), tc.want))
		})
	}
}

func TestInteractiveEnvDisabled(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "no env", env: nil, want: false},
		{name: "CI set", env: map[string]string{"CI": "true"}, want: true},
		{name: "CIRCLE_NO_INTERACTIVE set", env: map[string]string{"CIRCLE_NO_INTERACTIVE": "1"}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearTerminalEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			assert.Check(t, cmp.Equal(interactiveEnvDisabled(), tc.want))
		})
	}
}

// clearLocaleEnv unsets every locale variable so a test starts from "no locale
// named", where the 24-hour default applies.
func clearLocaleEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"LC_ALL", "LC_TIME", "LANG"} {
		t.Setenv(k, "")
	}
}

// TestClockLayout pins which locales get a 12-hour clock. The region is what
// decides; the language only matters for the one locale whose cycle differs from
// its region's (fr-CA). The default when nothing is named is 24-hour.
func TestClockLayout(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "no locale named", env: nil, want: clockLayout24},
		{name: "C means no locale", env: map[string]string{"LANG": "C"}, want: clockLayout24},
		{name: "US English is 12-hour", env: map[string]string{"LANG": "en_US.UTF-8"}, want: clockLayout12},
		// A bare language names no region, so the default applies even where the
		// language's home region is 12-hour.
		{name: "bare language has no region", env: map[string]string{"LANG": "en"}, want: clockLayout24},
		{name: "India is 12-hour", env: map[string]string{"LANG": "en_IN"}, want: clockLayout12},
		{name: "UK is 24-hour", env: map[string]string{"LANG": "en_GB.UTF-8"}, want: clockLayout24},
		{name: "Canada is 12-hour", env: map[string]string{"LANG": "en_CA.UTF-8"}, want: clockLayout12},
		// The one language-and-region override: Canada is 12-hour, Québec is not.
		{name: "Quebec is 24-hour", env: map[string]string{"LANG": "fr_CA.UTF-8"}, want: clockLayout24},
		{name: "Japan is 24-hour", env: map[string]string{"LANG": "ja_JP.UTF-8"}, want: clockLayout24},
		{name: "hyphen separator works", env: map[string]string{"LANG": "en-CA"}, want: clockLayout12},
		// LC_ALL wins over LC_TIME, which wins over LANG — the POSIX precedence.
		{name: "LC_TIME beats LANG", env: map[string]string{"LANG": "en_US", "LC_TIME": "en_GB"}, want: clockLayout24},
		{name: "LC_ALL beats LC_TIME", env: map[string]string{"LC_ALL": "en_CA", "LC_TIME": "en_GB"}, want: clockLayout12},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearLocaleEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			assert.Check(t, cmp.Equal(ClockLayout(), tc.want))
		})
	}
}

// TestSplitLocale pins how a POSIX locale string is parsed: the codeset and
// modifier suffixes dropped, the underscore accepted, the language lowercased
// and the region uppercased.
func TestSplitLocale(t *testing.T) {
	tests := []struct {
		locale, lang, region string
	}{
		{"en_US.UTF-8", "en", "US"},
		{"fr_CA@euro", "fr", "CA"},
		{"en-CA", "en", "CA"},
		{"en", "en", ""},
		{"C", "c", ""},
		{"", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.locale, func(t *testing.T) {
			lang, region := splitLocale(tc.locale)
			assert.Check(t, cmp.Equal(lang, tc.lang))
			assert.Check(t, cmp.Equal(region, tc.region))
		})
	}
}

func TestBackgroundQueryableNonTerminal(t *testing.T) {
	clearTerminalEnv(t)

	// A pipe is not a terminal; querying it would be pointless, so it must be
	// reported non-queryable even in an otherwise interactive environment.
	pr, pw, err := os.Pipe()
	assert.NilError(t, err)
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	assert.Check(t, !backgroundQueryable(pr, pw))
}

// TestEnvAllowsInteractive pins the exported inverse of
// interactiveEnvDisabled. It is the seam a caller with no TTY uses to decide
// whether an action that still needs a human — opening a browser to finish an
// OAuth flow — is worth attempting at all, so both the inversion and its
// independence from TTY state matter.
func TestEnvAllowsInteractive(t *testing.T) {
	t.Run("allowed by default", func(t *testing.T) {
		clearTerminalEnv(t)
		assert.Check(t, EnvAllowsInteractive())
	})

	t.Run("CI suppresses", func(t *testing.T) {
		clearTerminalEnv(t)
		t.Setenv("CI", "true")
		assert.Check(t, !EnvAllowsInteractive())
	})

	t.Run("CIRCLE_NO_INTERACTIVE suppresses", func(t *testing.T) {
		clearTerminalEnv(t)
		t.Setenv("CIRCLE_NO_INTERACTIVE", "1")
		assert.Check(t, !EnvAllowsInteractive())
	})

	t.Run("true where IsInteractive is false for want of a TTY", func(t *testing.T) {
		clearTerminalEnv(t)

		pr, pw, err := os.Pipe()
		assert.NilError(t, err)
		t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })

		s := Streams{In: pr, Out: pw, Err: pw}
		assert.Check(t, !s.IsInteractive(), "a pipe is not a TTY")
		assert.Check(t, EnvAllowsInteractive(), "yet the environment permits interactivity")
	})
}

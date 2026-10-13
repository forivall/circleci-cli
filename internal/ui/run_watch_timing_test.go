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
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// This file tests the watch footer's two timing rules as the pure functions they
// are, from inside the package. They are what a run watcher stares at for
// minutes on end, and both are defined at sub-second resolution — proving them
// through the program loop would mean a test that sleeps through real seconds and
// still only samples the behaviour. The flow's own behaviour is driven through
// teatest in run_watch_flow_test.go, as agents/14-testing.md requires.

// TestCeilSeconds pins the countdown's rounding rule. Rounding to the nearest
// second would drop the figure half a second early and run the count out with a
// second still to go, which reads as the timer skipping and the run refreshing
// ahead of its own countdown.
func TestCeilSeconds(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"a whole second is already exact", 5 * time.Second, 5 * time.Second},
		{"a fraction of a second still has a second to run", 400 * time.Millisecond, time.Second},
		{"a part second rounds up, never down", 19*time.Second + 400*time.Millisecond, 20 * time.Second},
		{"the last instant of a second belongs to it", 19*time.Second + 999*time.Millisecond, 20 * time.Second},
		{"zero is zero", 0, 0},
		{"an overdue poll has nothing left to count", -400 * time.Millisecond, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.Equal(ceilSeconds(tc.in), tc.want))
		})
	}
}

// TestPollMeterFill pins what the footer's meter reads through a wait: empty when
// the whole wait is ahead, full for its last second, and clamped rather than
// mis-drawn when the figures do not line up (a wait not yet scheduled, or a
// remaining time outside it).
func TestPollMeterFill(t *testing.T) {
	for _, tc := range []struct {
		name            string
		wait, remaining time.Duration
		want            string
	}{
		{"the whole wait is ahead", 12 * time.Second, 12 * time.Second, "▱▱▱▱▱▱"},
		{"a sixth in", 12 * time.Second, 10 * time.Second, "▰▱▱▱▱▱"},
		{"halfway", 12 * time.Second, 6 * time.Second, "▰▰▰▱▱▱"},
		{"the last second reads full", 12 * time.Second, time.Second, "▰▰▰▰▰▰"},
		{"the shortest wait still spans the meter", 5 * time.Second, 5 * time.Second, "▱▱▱▱▱▱"},
		{"the longest one too", 30 * time.Second, time.Second, "▰▰▰▰▰▰"},
		{"no wait scheduled yet", 0, 0, "▱▱▱▱▱▱"},
		{"more remaining than the wait", 12 * time.Second, 20 * time.Second, "▱▱▱▱▱▱"},
		{"an overdue poll", 12 * time.Second, -5 * time.Second, "▰▰▰▰▰▰"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := RunWatchFlowModel{wait: tc.wait}
			assert.Check(t, cmp.Equal(m.pollMeter(tc.remaining), tc.want))
		})
	}
}

// TestTimingColumn pins what a row's trailing column reads. The three cases are
// drawn differently on purpose: an ended row reports its own duration and the
// window it ran in, a running one counts up from its start (so it moves with the
// footer's elapsed clock rather than freezing at the last poll's figure), and a
// row that has not started shows nothing at all.
//
// The times are built in the local zone the column renders in, so the clock
// figures are asserted as written rather than shifted by the test machine's
// timezone.
func TestTimingColumn(t *testing.T) {
	m := RunWatchFlowModel{}
	start := time.Date(2026, 1, 2, 15, 4, 5, 0, time.Local)

	t.Run("an ended row reports its duration and window", func(t *testing.T) {
		end := start.Add(62 * time.Second)
		got := m.timing(&start, &end)

		assert.Check(t, cmp.Contains(got, "1m2s"))
		assert.Check(t, cmp.Contains(got, "@ 15:04:05 → 15:05:07"))
	})

	t.Run("a running row counts up from its start", func(t *testing.T) {
		running := time.Now().Add(-2 * time.Minute)
		got := m.timing(&running, nil)

		assert.Check(t, cmp.Contains(got, "2m0s"), "a running row counts elapsed time")
		assert.Check(t, !strings.Contains(got, "→"), "a row still going has no end time to show")
	})

	t.Run("a row that has not started has no column", func(t *testing.T) {
		notStarted := time.Time{}
		assert.Check(t, cmp.Equal(m.timing(&notStarted, nil), ""))
	})
}

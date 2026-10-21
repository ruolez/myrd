// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"math"
	"testing"
	"time"
)

func TestRateControllerRun(t *testing.T) {
	cases := []struct {
		name           string
		initialBitrate int
		usage          []usage
		expected       []DelayStats
	}{
		{
			name:           "empty",
			initialBitrate: 100_000,
			usage:          []usage{},
			expected:       []DelayStats{},
		},
		{
			name:           "increasesMultiplicativelyBy8000",
			initialBitrate: 100_000,
			usage:          []usage{usageNormal, usageNormal},
			expected: []DelayStats{{
				Usage:         usageNormal,
				State:         stateIncrease,
				TargetBitrate: 108_000,
				Estimate:      0,
				Threshold:     0,
			}},
		},
	}

	t0 := time.Time{}.Add(time.Hour)
	mockNoFn := func() time.Time {
		t0 = t0.Add(100 * time.Millisecond)

		return t0
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := make(chan DelayStats)
			dc := newRateController(mockNoFn, 100_000, 1_000, 50_000_000, func(ds DelayStats) {
				out <- ds
			})
			in := make(chan DelayStats)
			dc.onReceivedRate(100_000)
			dc.updateRTT(300 * time.Millisecond)
			go func() {
				defer close(out)
				for _, state := range tc.usage {
					dc.onDelayStats(DelayStats{
						Measurement:   0,
						Estimate:      0,
						Threshold:     0,
						Usage:         state,
						State:         0,
						TargetBitrate: 0,
					})
				}
				close(in)
			}()
			received := []DelayStats{}
			for ds := range out {
				received = append(received, ds)
			}
			if len(tc.expected) > 0 {
				assertEqual(t, tc.expected[0], received[0])
			}
		})
	}
}

func TestRateControllerIncrease(t *testing.T) {
	now := time.Unix(1000, 0)
	cases := []struct {
		name     string
		target   int
		received int
		decrease *exponentialMovingAverage // nil: multiplicative path
		want     int
	}{
		{"app-limited holds", 10_000_000, 1_000_000, nil, 10_000_000},
		{"capped at 1.5x received + 10k", 10_000_000, 7_000_000, nil, 10_510_000},
		{"8% per second when validated", 10_000_000, 10_000_000, nil, 10_800_000},
		{"additive step near the last decrease", 2_000_000, 2_000_000,
			&exponentialMovingAverage{average: 2_000_000, stdDeviation: 100_000}, 2_004_761},
		{"additive never lowers while app-limited", 2_000_000, 1_000_000,
			&exponentialMovingAverage{average: 1_000_000, stdDeviation: 100_000}, 2_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newRateController(time.Now, tc.target, 10_000, 50_000_000, func(DelayStats) {})
			c.latestReceivedRate = tc.received
			c.lastUpdate = now.Add(-time.Second)
			if tc.decrease != nil {
				c.latestDecreaseRate = tc.decrease
			}
			assertEqual(t, tc.want, c.increase(now))
			assertEqual(t, now, c.lastUpdate)
		})
	}
}

func TestRateControllerDecrease(t *testing.T) {
	now := time.Unix(1000, 0)
	cases := []struct {
		name     string
		target   int
		received int
		want     int
		wantEMA  exponentialMovingAverage
	}{
		{"app-limited backs off from the target", 10_000_000, 1_000_000, 8_500_000,
			exponentialMovingAverage{average: 3_000_000}},
		{"from the received rate", 10_000_000, 8_000_000, 6_800_000,
			exponentialMovingAverage{average: 3_000_000 + 0.95*5_000_000, variance: 0.05 * 0.95 * 5_000_000 * 5_000_000,
				stdDeviation: math.Sqrt(0.05 * 0.95 * 5_000_000 * 5_000_000)}},
		{"received at exactly half the target counts", 10_000_000, 5_000_000, 4_250_000,
			exponentialMovingAverage{average: 3_000_000 + 0.95*2_000_000, variance: 0.05 * 0.95 * 2_000_000 * 2_000_000,
				stdDeviation: math.Sqrt(0.05 * 0.95 * 2_000_000 * 2_000_000)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newRateController(func() time.Time { return now }, tc.target, 10_000, 50_000_000, func(DelayStats) {})
			c.latestReceivedRate = tc.received
			c.latestDecreaseRate = &exponentialMovingAverage{average: 3_000_000}
			assertEqual(t, tc.want, c.decrease())
			assertInDelta(t, tc.wantEMA.average, c.latestDecreaseRate.average, 1e-6)
			assertInDelta(t, tc.wantEMA.variance, c.latestDecreaseRate.variance, 1e-3)
			assertInDelta(t, tc.wantEMA.stdDeviation, c.latestDecreaseRate.stdDeviation, 1e-6)
			assertEqual(t, now, c.lastUpdate)
		})
	}
}

func TestRateControllerOnProbe(t *testing.T) {
	stats := DelayStats{Estimate: time.Millisecond, Usage: usageNormal, State: stateIncrease}
	cases := []struct {
		name   string
		init   bool
		usage  usage
		max    int
		rate   int
		wantOK bool
		want   int
	}{
		{"before any delay stats", false, usageNormal, 50_000_000, 2_000_000, false, 1_000_000},
		{"overuse", true, usageOver, 50_000_000, 2_000_000, false, 1_000_000},
		{"raises to the probe", true, usageNormal, 50_000_000, 2_000_000, true, 2_000_000},
		{"underuse raises too", true, usageUnder, 50_000_000, 2_000_000, true, 2_000_000},
		{"at most 3x at once", true, usageNormal, 50_000_000, 9_000_000, true, 3_000_000},
		{"clamped to max", true, usageNormal, 2_500_000, 9_000_000, true, 2_500_000},
		{"never lowers", true, usageNormal, 50_000_000, 500_000, false, 1_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newRateController(time.Now, 1_000_000, 10_000, tc.max, func(DelayStats) {})
			c.init = tc.init
			c.delayStats = stats
			c.delayStats.Usage = tc.usage
			ds, ok := c.onProbe(tc.rate)
			assertEqual(t, tc.wantOK, ok)
			assertEqual(t, tc.want, c.target)
			if tc.wantOK {
				want := stats
				want.Usage = tc.usage
				want.TargetBitrate = tc.want
				assertEqual(t, want, ds)
			}
		})
	}
}

// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"math"
	"sync"
	"time"
)

const (
	decreaseEMAAlpha = 0.95
	beta             = 0.85
)

type rateController struct {
	now                  now
	initialTargetBitrate int
	minBitrate           int
	maxBitrate           int

	dsWriter func(DelayStats)

	lock               sync.Mutex
	init               bool
	delayStats         DelayStats
	target             int
	lastUpdate         time.Time
	lastState          state
	latestRTT          time.Duration
	latestReceivedRate int
	latestDecreaseRate *exponentialMovingAverage
}

type exponentialMovingAverage struct {
	average      float64
	variance     float64
	stdDeviation float64
}

func (a *exponentialMovingAverage) update(value float64) {
	if a.average == 0.0 {
		a.average = value
	} else {
		x := value - a.average
		a.average += decreaseEMAAlpha * x
		a.variance = (1 - decreaseEMAAlpha) * (a.variance + decreaseEMAAlpha*x*x)
		a.stdDeviation = math.Sqrt(a.variance)
	}
}

func newRateController(
	now now, initialTargetBitrate, minBitrate, maxBitrate int, dsw func(DelayStats),
) *rateController {
	return &rateController{
		now:                  now,
		initialTargetBitrate: initialTargetBitrate,
		minBitrate:           minBitrate,
		maxBitrate:           maxBitrate,
		dsWriter:             dsw,
		init:                 false,
		delayStats:           DelayStats{},
		target:               initialTargetBitrate,
		lastUpdate:           time.Time{},
		lastState:            stateIncrease,
		latestRTT:            0,
		latestReceivedRate:   0,
		latestDecreaseRate:   &exponentialMovingAverage{},
	}
}

func (c *rateController) onReceivedRate(rate int) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.latestReceivedRate = rate
}

func (c *rateController) updateRTT(rtt time.Duration) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.latestRTT = rtt
}

func (c *rateController) onDelayStats(ds DelayStats) {
	c.lock.Lock()
	if !c.init {
		c.delayStats = ds
		c.delayStats.State = stateIncrease
		c.init = true
		c.lock.Unlock()

		return
	}
	c.delayStats = ds
	c.delayStats.State = c.delayStats.State.transition(ds.Usage)

	switch c.delayStats.State {
	case stateHold:
		c.lock.Unlock()

		return
	case stateIncrease:
		c.target = clampInt(c.increase(c.now()), c.minBitrate, c.maxBitrate)
	case stateDecrease:
		c.target = clampInt(c.decrease(), c.minBitrate, c.maxBitrate)
	}
	next := c.delayStats
	next.TargetBitrate = c.target
	c.lock.Unlock()

	c.dsWriter(next)
}

// onProbe raises the target to a rate a burst got through at, at most 3x
// at once. It reports the new stats, or false when the target did not move
// or the delay detector is not known to be clear of overuse.
func (c *rateController) onProbe(rate int) (DelayStats, bool) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if !c.init || c.delayStats.Usage == usageOver {
		return DelayStats{}, false
	}
	next := clampInt(max(c.target, min(rate, 3*c.target)), c.minBitrate, c.maxBitrate)
	if next <= c.target {
		return DelayStats{}, false
	}
	c.target = next
	ds := c.delayStats
	ds.TargetBitrate = next

	return ds, true
}

// increase never goes above 1.5x the received rate (+10 kbps): while the
// sender is application-limited nothing validates a higher rate, so the
// target holds (libwebrtc's AimdRateControl rule).
func (c *rateController) increase(now time.Time) int {
	limit := max(c.target, int(1.5*float64(c.latestReceivedRate))+10_000)
	if c.latestDecreaseRate.average > 0 &&
		float64(c.latestReceivedRate) > c.latestDecreaseRate.average-3*c.latestDecreaseRate.stdDeviation &&
		float64(c.latestReceivedRate) < c.latestDecreaseRate.average+3*c.latestDecreaseRate.stdDeviation {
		bitsPerFrame := float64(c.target) / 30.0
		packetsPerFrame := math.Ceil(bitsPerFrame / (1200 * 8))
		expectedPacketSizeBits := bitsPerFrame / packetsPerFrame

		responseTime := 100*time.Millisecond + c.latestRTT
		alpha := 0.5 * math.Min(float64(now.Sub(c.lastUpdate).Milliseconds())/float64(responseTime.Milliseconds()), 1.0)
		increase := int(math.Max(1000.0, alpha*expectedPacketSizeBits))
		c.lastUpdate = now

		return min(c.target+increase, limit)
	}
	eta := math.Pow(1.08, math.Min(float64(now.Sub(c.lastUpdate).Milliseconds())/1000, 1.0))
	c.lastUpdate = now

	return min(max(int(eta*float64(c.target)), c.target), limit)
}

// decrease backs off from the received rate, or from the target while the
// sender is application-limited (received < target/2): then the received
// rate says nothing about capacity and must not feed latestDecreaseRate.
func (c *rateController) decrease() int {
	c.lastUpdate = c.now()
	if c.latestReceivedRate < c.target/2 {
		return int(beta * float64(c.target))
	}
	c.latestDecreaseRate.update(float64(c.latestReceivedRate))

	return int(beta * float64(c.latestReceivedRate))
}

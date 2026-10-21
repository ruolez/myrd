// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"sync"
	"time"

	"github.com/pion/logging"
)

const (
	// constants from
	// https://datatracker.ietf.org/doc/html/draft-ietf-rmcat-gcc-02#section-6

	increaseLossThreshold = 0.02
	increaseTimeThreshold = 200 * time.Millisecond
	increaseFactor        = 1.05

	decreaseLossThreshold = 0.1
	decreaseTimeThreshold = 200 * time.Millisecond

	// A loss decision rests on at least lossWindowPackets packets and, at low
	// packet rates, up to lossWindow of feedback (lossWindowFull packets end
	// it early). One feedback report covers a few dozen packets, so a single
	// lost packet there read as 5-40% loss: random 5% loss kept tripping the
	// 10% decrease and never allowed an increase, and the estimate sank to
	// the floor. libwebrtc decides on similar windows.
	lossWindowPackets = 20
	lossWindowFull    = 100
	lossWindow        = time.Second
)

// LossStats contains internal statistics of the loss based controller.
type LossStats struct {
	TargetBitrate int
	AverageLoss   float64
}

type lossBasedBandwidthEstimator struct {
	lock           sync.Mutex
	now            func() time.Time
	maxBitrate     int
	minBitrate     int
	bitrate        int
	averageLoss    float64 // loss of the last complete window
	lastLossUpdate time.Time
	lastIncrease   time.Time
	lastDecrease   time.Time
	winStart       time.Time
	winLost        int
	winTotal       int
	log            logging.LeveledLogger
}

func newLossBasedBWE(initialBitrate, minBitrate, maxBitrate int, loggerFactory logging.LoggerFactory) *lossBasedBandwidthEstimator {
	return &lossBasedBandwidthEstimator{
		lock:       sync.Mutex{},
		now:        time.Now,
		maxBitrate: maxBitrate,
		minBitrate: minBitrate,
		bitrate:    initialBitrate,
		log:        loggerFactory.NewLogger("gcc_loss_controller"),
	}
}

func (e *lossBasedBandwidthEstimator) getEstimate(wantedRate int) LossStats {
	e.lock.Lock()
	defer e.lock.Unlock()

	if e.bitrate <= 0 {
		e.bitrate = clampInt(wantedRate, e.minBitrate, e.maxBitrate)
	}
	e.bitrate = min(wantedRate, e.bitrate)

	return LossStats{
		TargetBitrate: e.bitrate,
		AverageLoss:   e.averageLoss,
	}
}

// raiseTo lifts the bitrate to a probed rate unless loss is too high to
// increase at all.
func (e *lossBasedBandwidthEstimator) raiseTo(rate int) {
	e.lock.Lock()
	defer e.lock.Unlock()
	if e.averageLoss < increaseLossThreshold {
		e.bitrate = max(e.bitrate, clampInt(rate, e.minBitrate, e.maxBitrate))
	}
}

func (e *lossBasedBandwidthEstimator) updateLossEstimate(results []Acknowledgment) {
	if len(results) == 0 {
		return
	}

	packetsLost := 0
	for _, p := range results {
		if p.Arrival.IsZero() {
			packetsLost++
		}
	}

	e.lock.Lock()
	defer e.lock.Unlock()

	now := e.now()
	if e.winTotal == 0 {
		e.winStart = now
	}
	e.winLost += packetsLost
	e.winTotal += len(results)
	if e.winTotal < lossWindowPackets || (e.winTotal < lossWindowFull && now.Sub(e.winStart) < lossWindow) {
		return
	}
	loss := float64(e.winLost) / float64(e.winTotal)
	e.winLost, e.winTotal = 0, 0
	e.averageLoss = loss
	e.lastLossUpdate = now

	if loss < increaseLossThreshold && now.Sub(e.lastIncrease) > increaseTimeThreshold {
		e.log.Infof("loss controller increasing; loss: %v", loss)
		e.lastIncrease = now
		e.bitrate = clampInt(int(increaseFactor*float64(e.bitrate)), e.minBitrate, e.maxBitrate)
	} else if loss > decreaseLossThreshold && now.Sub(e.lastDecrease) > decreaseTimeThreshold {
		e.log.Infof("loss controller decreasing; loss: %v", loss)
		e.lastDecrease = now
		e.bitrate = clampInt(int(float64(e.bitrate)*(1-0.5*loss)), e.minBitrate, e.maxBitrate)
	}
}

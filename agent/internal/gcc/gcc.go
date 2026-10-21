// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

// Package gcc implements Google Congestion Control for bandwidth estimation.
//
// It is forked from github.com/pion/interceptor v0.1.49 pkg/gcc (with
// internal/cc's feedback adapter and internal/ntp merged in) for a remote
// desktop stream, which is application-limited most of the time:
//
//   - The delay-based increase never goes above max(target, 1.5×received +
//     10 kbps), as libwebrtc's AimdRateControl, so a static screen holds the
//     estimate instead of letting it climb to max unvalidated (pion issue #299).
//   - A decrease while the received rate is under half the target cuts the
//     target by beta and leaves the link-capacity average alone: the low rate
//     says nothing about the link.
//   - Every large frame leaves the pacer as a tight burst, so it is used as a
//     bandwidth probe (libwebrtc's ProbeBitrateEstimator); a probe raises the
//     target to up to 3× at once instead of 8%/s.
//   - A pacer is mandatory (the leaky bucket and no-op pacers are dropped),
//     the feedback history holds more packets, and GetStats reports "rtt".
package gcc

import "time"

func clampInt(b, minVal, maxVal int) int {
	return max(minVal, min(maxVal, b))
}

func clampDuration(d, minVal, maxVal time.Duration) time.Duration {
	return time.Duration(clampInt(int(d), int(minVal), int(maxVal)))
}

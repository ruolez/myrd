// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"time"
)

// ntpToTime converts a uint64 NTP timestamp to a time.Time.
func ntpToTime(t uint64) time.Time {
	seconds := (t & 0xFFFFFFFF00000000) >> 32
	fractional := float64(t&0x00000000FFFFFFFF) / float64(0xFFFFFFFF)
	d := time.Duration(seconds)*time.Second + time.Duration(fractional*1e9)*time.Nanosecond

	return time.Unix(0, 0).Add(-2208988800 * time.Second).Add(d)
}

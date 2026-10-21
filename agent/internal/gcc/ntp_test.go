// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"fmt"
	"testing"
	"time"
)

// toNTP is pion's ntp.ToNTP, the inverse of ntpToTime.
func toNTP(t time.Time) uint64 {
	s := (float64(t.UnixNano()) / 1000000000) + 2208988800
	integerPart := uint32(s)
	fractionalPart := uint32((s - float64(integerPart)) * 0xFFFFFFFF)

	return uint64(integerPart)<<32 | uint64(fractionalPart)
}

func TestNTPToTimeConverstion(t *testing.T) {
	for i, cc := range []struct {
		ts time.Time
	}{
		{
			ts: time.Now(),
		},
		{
			ts: time.Unix(0, 0),
		},
	} {
		t.Run(fmt.Sprintf("TimeToNTP/%v", i), func(t *testing.T) {
			assertInDelta(t, cc.ts.UnixNano(), ntpToTime(toNTP(cc.ts)).UnixNano(), float64(time.Millisecond.Nanoseconds()))
		})
	}
}

func TestTimeToNTPConverstion(t *testing.T) {
	for i, cc := range []struct {
		ts uint64
	}{
		{
			ts: 0,
		},
		{
			ts: 65535,
		},
		{
			ts: 16606669245815957503,
		},
		{
			ts: 9487534653230284800,
		},
	} {
		t.Run(fmt.Sprintf("TimeToNTP/%v", i), func(t *testing.T) {
			assertEqual(t, cc.ts, toNTP(ntpToTime(cc.ts)))
		})
	}
}

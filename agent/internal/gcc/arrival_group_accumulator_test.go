// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"testing"
	"time"
)

func TestArrivalGroupAccumulator(t *testing.T) {
	triggerNewGroupElement := Acknowledgment{
		Departure: time.Time{}.Add(time.Second),
		Arrival:   time.Time{}.Add(time.Second),
	}
	cases := []struct {
		name string
		log  []Acknowledgment
		exp  []arrivalGroup
	}{
		{
			name: "emptyCreatesNoGroups",
			log:  []Acknowledgment{},
			exp:  []arrivalGroup{},
		},
		{
			name: "createsSingleElementGroup",
			log: []Acknowledgment{
				{
					Departure: time.Time{},
					Arrival:   time.Time{}.Add(time.Millisecond),
				},
				triggerNewGroupElement,
			},
			exp: []arrivalGroup{
				{
					packets: []Acknowledgment{{
						Departure: time.Time{},
						Arrival:   time.Time{}.Add(time.Millisecond),
					}},
					arrival:   time.Time{}.Add(time.Millisecond),
					departure: time.Time{},
				},
			},
		},
		{
			name: "createsTwoElementGroup",
			log: []Acknowledgment{
				{
					Arrival: time.Time{}.Add(15 * time.Millisecond),
				},
				{
					Departure: time.Time{}.Add(3 * time.Millisecond),
					Arrival:   time.Time{}.Add(20 * time.Millisecond),
				},
				triggerNewGroupElement,
			},
			exp: []arrivalGroup{{
				packets: []Acknowledgment{
					{
						Departure: time.Time{},
						Arrival:   time.Time{}.Add(15 * time.Millisecond),
					},
					{
						Departure: time.Time{}.Add(3 * time.Millisecond),
						Arrival:   time.Time{}.Add(20 * time.Millisecond),
					},
				},
				arrival:   time.Time{}.Add(20 * time.Millisecond),
				departure: time.Time{},
			}},
		},
		{
			name: "createsTwoArrivalGroups",
			log: []Acknowledgment{
				{
					Departure: time.Time{},
					Arrival:   time.Time{}.Add(15 * time.Millisecond),
				},
				{
					Departure: time.Time{}.Add(3 * time.Millisecond),
					Arrival:   time.Time{}.Add(20 * time.Millisecond),
				},
				{
					Departure: time.Time{}.Add(9 * time.Millisecond),
					Arrival:   time.Time{}.Add(30 * time.Millisecond),
				},
				triggerNewGroupElement,
			},
			exp: []arrivalGroup{
				{
					packets: []Acknowledgment{
						{
							Arrival: time.Time{}.Add(15 * time.Millisecond),
						},
						{
							Departure: time.Time{}.Add(3 * time.Millisecond),
							Arrival:   time.Time{}.Add(20 * time.Millisecond),
						},
					},
					arrival:   time.Time{}.Add(20 * time.Millisecond),
					departure: time.Time{}.Add(0 * time.Millisecond),
				},
				{
					packets: []Acknowledgment{
						{
							Departure: time.Time{}.Add(9 * time.Millisecond),
							Arrival:   time.Time{}.Add(30 * time.Millisecond),
						},
					},
					arrival:   time.Time{}.Add(30 * time.Millisecond),
					departure: time.Time{}.Add(9 * time.Millisecond),
				},
			},
		},
		{
			name: "ignoresOutOfOrderPackets",
			log: []Acknowledgment{
				{
					Departure: time.Time{},
					Arrival:   time.Time{}.Add(15 * time.Millisecond),
				},
				{
					Departure: time.Time{}.Add(6 * time.Millisecond),
					Arrival:   time.Time{}.Add(34 * time.Millisecond),
				},
				{
					Departure: time.Time{}.Add(8 * time.Millisecond),
					Arrival:   time.Time{}.Add(30 * time.Millisecond),
				},
				triggerNewGroupElement,
			},
			exp: []arrivalGroup{
				{
					packets: []Acknowledgment{
						{
							Departure: time.Time{},
							Arrival:   time.Time{}.Add(15 * time.Millisecond),
						},
					},
					arrival:   time.Time{}.Add(15 * time.Millisecond),
					departure: time.Time{},
				},
				{
					packets: []Acknowledgment{
						{
							Departure: time.Time{}.Add(6 * time.Millisecond),
							Arrival:   time.Time{}.Add(34 * time.Millisecond),
						},
					},
					arrival:   time.Time{}.Add(34 * time.Millisecond),
					departure: time.Time{}.Add(6 * time.Millisecond),
				},
			},
		},
		{
			name: "newGroupBecauseOfInterDepartureTime",
			log: []Acknowledgment{
				{
					SequenceNumber: 0,
					Departure:      time.Time{},
					Arrival:        time.Time{}.Add(4 * time.Millisecond),
				},
				{
					SequenceNumber: 1,
					Departure:      time.Time{}.Add(3 * time.Millisecond),
					Arrival:        time.Time{}.Add(4 * time.Millisecond),
				},
				{
					SequenceNumber: 2,
					Departure:      time.Time{}.Add(6 * time.Millisecond),
					Arrival:        time.Time{}.Add(10 * time.Millisecond),
				},
				{
					SequenceNumber: 3,
					Departure:      time.Time{}.Add(9 * time.Millisecond),
					Arrival:        time.Time{}.Add(10 * time.Millisecond),
				},
				triggerNewGroupElement,
			},
			exp: []arrivalGroup{
				{
					packets: []Acknowledgment{
						{
							SequenceNumber: 0,
							Departure:      time.Time{},
							Arrival:        time.Time{}.Add(4 * time.Millisecond),
						},
						{
							SequenceNumber: 1,
							Departure:      time.Time{}.Add(3 * time.Millisecond),
							Arrival:        time.Time{}.Add(4 * time.Millisecond),
						},
					},
					departure: time.Time{},
					arrival:   time.Time{}.Add(4 * time.Millisecond),
				},
				{
					packets: []Acknowledgment{
						{
							SequenceNumber: 2,
							Departure:      time.Time{}.Add(6 * time.Millisecond),
							Arrival:        time.Time{}.Add(10 * time.Millisecond),
						},
						{
							SequenceNumber: 3,
							Departure:      time.Time{}.Add(9 * time.Millisecond),
							Arrival:        time.Time{}.Add(10 * time.Millisecond),
						},
					},
					departure: time.Time{}.Add(6 * time.Millisecond),
					arrival:   time.Time{}.Add(10 * time.Millisecond),
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aga := newArrivalGroupAccumulator()
			in := make(chan []Acknowledgment)
			out := make(chan arrivalGroup)
			go func() {
				defer close(out)
				aga.run(in, func(ag arrivalGroup) {
					out <- ag
				})
			}()
			go func() {
				in <- tc.log
				close(in)
			}()
			received := []arrivalGroup{}
			for g := range out {
				received = append(received, g)
			}
			assertEqual(t, tc.exp, received)
		})
	}
}

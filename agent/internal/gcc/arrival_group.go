// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"fmt"
	"time"
)

type arrivalGroup struct {
	packets   []Acknowledgment
	departure time.Time
	arrival   time.Time
}

func newArrivalGroup(a Acknowledgment) arrivalGroup {
	return arrivalGroup{
		packets:   []Acknowledgment{a},
		departure: a.Departure,
		arrival:   a.Arrival,
	}
}

func (g *arrivalGroup) add(a Acknowledgment) {
	g.packets = append(g.packets, a)
	g.arrival = a.Arrival
}

func (g arrivalGroup) String() string {
	s := "ARRIVALGROUP:\n"
	s += fmt.Sprintf("\tARRIVAL:\t%v\n", int64(float64(g.arrival.UnixNano())/1e+6))
	s += fmt.Sprintf("\tDEPARTURE:\t%v\n", int64(float64(g.departure.UnixNano())/1e+6))
	s += fmt.Sprintf("\tPACKETS:\n%v\n", g.packets)

	return s
}

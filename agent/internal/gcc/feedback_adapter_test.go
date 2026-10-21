// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"fmt"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

const hdrExtID = uint8(1)

func TestUnpackRunLengthChunk(t *testing.T) {
	attributes := make(interceptor.Attributes)
	attributes.Set(TwccExtensionAttributesKey, hdrExtID)

	cases := []struct {
		sentTLCC []uint16
		chunk    rtcp.RunLengthChunk
		deltas   []*rtcp.RecvDelta
		start    uint16
		// expect:
		acks    []Acknowledgment
		refTime time.Time
		n       int
	}{
		{
			sentTLCC: []uint16{},
			chunk:    rtcp.RunLengthChunk{},
			deltas:   []*rtcp.RecvDelta{},
			start:    0,
			acks:     []Acknowledgment{},
			refTime:  time.Time{},
			n:        0,
		},
		{
			sentTLCC: []uint16{0, 1, 2, 3, 4, 5},
			chunk: rtcp.RunLengthChunk{
				PacketStatusChunk:  nil,
				Type:               0,
				PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta,
				RunLength:          6,
			},
			deltas: []*rtcp.RecvDelta{
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 0},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 0},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 0},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 0},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 0},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 0},
			},
			start: 0,
			acks: []Acknowledgment{
				{
					SequenceNumber: 0,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 1,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 2,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 3,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 4,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 5,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
			},
			n:       6,
			refTime: time.Time{},
		},
		{
			sentTLCC: []uint16{65534, 65535, 0, 1, 2, 3},
			chunk: rtcp.RunLengthChunk{
				PacketStatusChunk:  nil,
				Type:               0,
				PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta,
				RunLength:          6,
			},
			deltas: []*rtcp.RecvDelta{
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250},
			},
			start: 65534,
			acks: []Acknowledgment{
				{
					SequenceNumber: 65534,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{}.Add(250 * time.Microsecond),
				},
				{
					SequenceNumber: 65535,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{}.Add(500 * time.Microsecond),
				},
				{
					SequenceNumber: 0,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{}.Add(750 * time.Microsecond),
				},
				{
					SequenceNumber: 1,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{}.Add(1000 * time.Microsecond),
				},
				{
					SequenceNumber: 2,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{}.Add(1250 * time.Microsecond),
				},
				{
					SequenceNumber: 3,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{}.Add(1500 * time.Microsecond),
				},
			},
			n:       6,
			refTime: time.Time{}.Add(1500 * time.Microsecond),
		},
		{
			sentTLCC: []uint16{65534, 65535, 0, 1, 2, 3},
			chunk: rtcp.RunLengthChunk{
				PacketStatusChunk:  nil,
				Type:               0,
				PacketStatusSymbol: rtcp.TypeTCCPacketNotReceived,
				RunLength:          6,
			},
			deltas: []*rtcp.RecvDelta{},
			start:  65534,
			acks: []Acknowledgment{
				{
					SequenceNumber: 65534,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 65535,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 0,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 1,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 2,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 3,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
			},
			n:       0,
			refTime: time.Time{},
		},
	}

	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			fa := NewFeedbackAdapter()

			headers := []*rtp.Header{}
			for i, nr := range tc.sentTLCC {
				headers = append(headers, &getPacketWithTransportCCExt(t, nr).Header)
				tc.acks[i].Size += headers[i].MarshalSize()
			}
			for _, h := range headers {
				assertNoError(t, fa.OnSent(time.Time{}, h, 0, attributes))
			}

			n, refTime, acks, err := fa.unpackRunLengthChunk(tc.start, time.Time{}, &tc.chunk, tc.deltas)
			assertNoError(t, err)
			assertLen(t, acks, len(tc.acks))
			assertEqual(t, tc.n, n)
			assertEqual(t, tc.refTime, refTime)

			for i, a := range acks {
				assertEqual(t, tc.sentTLCC[i], a.SequenceNumber)
			}
			assertEqual(t, tc.acks, acks)
		})
	}
}

func TestUnpackStatusVectorChunk(t *testing.T) {
	attributes := make(interceptor.Attributes)
	attributes.Set(TwccExtensionAttributesKey, hdrExtID)

	cases := []struct {
		sentTLCC []uint16
		chunk    rtcp.StatusVectorChunk
		deltas   []*rtcp.RecvDelta
		start    uint16
		// expect:
		acks    []Acknowledgment
		n       int
		refTime time.Time
	}{
		{
			sentTLCC: []uint16{},
			chunk:    rtcp.StatusVectorChunk{},
			deltas:   []*rtcp.RecvDelta{},
			start:    0,
			acks:     []Acknowledgment{},
			n:        0,
			refTime:  time.Time{},
		},
		{
			sentTLCC: []uint16{0, 1, 2, 3, 4, 5},
			chunk: rtcp.StatusVectorChunk{
				PacketStatusChunk: nil,
				Type:              0,
				SymbolSize:        rtcp.TypeTCCSymbolSizeTwoBit,
				SymbolList: []uint16{
					rtcp.TypeTCCPacketReceivedSmallDelta,
					rtcp.TypeTCCPacketReceivedSmallDelta,
					rtcp.TypeTCCPacketReceivedSmallDelta,
					rtcp.TypeTCCPacketReceivedSmallDelta,
					rtcp.TypeTCCPacketReceivedSmallDelta,
					rtcp.TypeTCCPacketReceivedSmallDelta,
				},
			},
			deltas: []*rtcp.RecvDelta{
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 0},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 0},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 0},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 0},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 0},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 0},
			},
			start: 0,
			acks: []Acknowledgment{
				{
					SequenceNumber: 0,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 1,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 2,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 3,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 4,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 5,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
			},
			n:       6,
			refTime: time.Time{},
		},
		{
			sentTLCC: []uint16{65534, 65535, 0, 1, 2, 3},
			chunk: rtcp.StatusVectorChunk{
				PacketStatusChunk: nil,
				Type:              0,
				SymbolSize:        rtcp.TypeTCCSymbolSizeTwoBit,
				SymbolList: []uint16{
					rtcp.TypeTCCPacketReceivedSmallDelta,
					rtcp.TypeTCCPacketReceivedSmallDelta,
					rtcp.TypeTCCPacketReceivedSmallDelta,
					rtcp.TypeTCCPacketReceivedSmallDelta,
					rtcp.TypeTCCPacketNotReceived,
					rtcp.TypeTCCPacketReceivedSmallDelta,
				},
			},
			deltas: []*rtcp.RecvDelta{
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250},
				{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250},
			},
			start: 65534,
			acks: []Acknowledgment{
				{
					SequenceNumber: 65534,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{}.Add(250 * time.Microsecond),
				},
				{
					SequenceNumber: 65535,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{}.Add(500 * time.Microsecond),
				},
				{
					SequenceNumber: 0,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{}.Add(750 * time.Microsecond),
				},
				{
					SequenceNumber: 1,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{}.Add(1000 * time.Microsecond),
				},
				{
					SequenceNumber: 2,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{},
				},
				{
					SequenceNumber: 3,
					Size:           0,
					Departure:      time.Time{},
					Arrival:        time.Time{}.Add(1250 * time.Microsecond),
				},
			},
			n:       5,
			refTime: time.Time{}.Add(1250 * time.Microsecond),
		},
	}

	for i, tc := range cases {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			fa := NewFeedbackAdapter()

			headers := []*rtp.Header{}
			for i, nr := range tc.sentTLCC {
				headers = append(headers, &getPacketWithTransportCCExt(t, nr).Header)
				tc.acks[i].Size += headers[i].MarshalSize()
			}
			for _, h := range headers {
				assertNoError(t, fa.OnSent(time.Time{}, h, 0, attributes))
			}

			n, refTime, acks, err := fa.unpackStatusVectorChunk(tc.start, time.Time{}, &tc.chunk, tc.deltas)
			assertNoError(t, err)
			assertLen(t, acks, len(tc.acks))
			assertEqual(t, tc.n, n)
			assertEqual(t, tc.refTime, refTime)

			for i, a := range acks {
				assertEqual(t, tc.sentTLCC[i], a.SequenceNumber)
			}
			assertEqual(t, tc.acks, acks)
		})
	}
}

func getPacketWithTransportCCExt(t *testing.T, sequenceNumber uint16) *rtp.Packet {
	t.Helper()

	pkt := rtp.Packet{
		Header:  rtp.Header{},
		Payload: []byte{},
	}
	ext := &rtp.TransportCCExtension{
		TransportSequence: sequenceNumber,
	}
	b, err := ext.Marshal()
	assertNoError(t, err)
	assertNoError(t, pkt.SetExtension(hdrExtID, b))

	return &pkt
}

func TestFeedbackAdapterTWCC(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		adapter := NewFeedbackAdapter()
		result, err := adapter.OnTransportCCFeedback(time.Time{}, &rtcp.TransportLayerCC{})
		assertNoError(t, err)
		assertEmpty(t, result)
	})

	t.Run("setsCorrectReceiveTime", func(t *testing.T) {
		t0 := time.Time{}
		adapter := NewFeedbackAdapter()
		headers := []rtp.Header{}
		for i := range uint16(22) {
			pkt := getPacketWithTransportCCExt(t, i)
			headers = append(headers, pkt.Header)
			assertNoError(
				t,
				adapter.OnSent(t0, &pkt.Header, 1200, interceptor.Attributes{TwccExtensionAttributesKey: hdrExtID}),
			)
		}
		results, err := adapter.OnTransportCCFeedback(t0, &rtcp.TransportLayerCC{
			Header:             rtcp.Header{},
			SenderSSRC:         0,
			MediaSSRC:          0,
			BaseSequenceNumber: 0,
			PacketStatusCount:  22,
			ReferenceTime:      0,
			FbPktCount:         0,
			PacketChunks: []rtcp.PacketStatusChunk{
				&rtcp.StatusVectorChunk{
					PacketStatusChunk: nil,
					Type:              rtcp.TypeTCCStatusVectorChunk,
					SymbolSize:        rtcp.TypeTCCSymbolSizeTwoBit,
					SymbolList: []uint16{
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketReceivedLargeDelta,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
					},
				},
				&rtcp.StatusVectorChunk{
					PacketStatusChunk: nil,
					Type:              rtcp.TypeTCCStatusVectorChunk,
					SymbolSize:        rtcp.TypeTCCSymbolSizeOneBit,
					SymbolList: []uint16{
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
					},
				},
				&rtcp.RunLengthChunk{
					Type:               rtcp.TypeTCCRunLengthChunk,
					PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta,
					RunLength:          1,
				},
			},
			RecvDeltas: []*rtcp.RecvDelta{
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedLargeDelta,
					Delta: 100,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 12,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
			},
		})

		assertNoError(t, err)

		assertNotEmpty(t, results)
		assertLen(t, results, 22)

		assertContains(t, results, Acknowledgment{
			SequenceNumber: 0,
			Size:           headers[0].MarshalSize() + 1200,
			Departure:      t0,
			Arrival:        t0.Add(4 * time.Microsecond),
		})

		assertContains(t, results, Acknowledgment{
			SequenceNumber: 1,
			Size:           headers[1].MarshalSize() + 1200,
			Departure:      t0,
			Arrival:        t0.Add(104 * time.Microsecond),
		})

		for i := uint16(2); i < 7; i++ {
			assertContains(t, results, Acknowledgment{
				SequenceNumber: i,
				Size:           headers[i].MarshalSize() + 1200,
				Departure:      t0,
				Arrival:        time.Time{},
			})
		}

		assertContains(t, results, Acknowledgment{
			SequenceNumber: 7,
			Size:           headers[7].MarshalSize() + 1200,
			Departure:      t0,
			Arrival:        t0.Add(116 * time.Microsecond),
		})

		for i := uint16(8); i < 21; i++ {
			assertContains(t, results, Acknowledgment{
				SequenceNumber: i,
				Size:           headers[i].MarshalSize() + 1200,
				Departure:      t0,
				Arrival:        time.Time{},
			})
		}

		assertContains(t, results, Acknowledgment{
			SequenceNumber: 21,
			Size:           headers[21].MarshalSize() + 1200,
			Departure:      t0,
			Arrival:        t0.Add(120 * time.Microsecond),
		})
	})

	t.Run("doesNotCrashOnTooManyFeedbackReports", func(*testing.T) {
		adapter := NewFeedbackAdapter()
		assertNotPanics(t, func() {
			_, err := adapter.OnTransportCCFeedback(time.Time{}, &rtcp.TransportLayerCC{
				Header:             rtcp.Header{},
				SenderSSRC:         0,
				MediaSSRC:          0,
				BaseSequenceNumber: 0,
				PacketStatusCount:  0,
				ReferenceTime:      0,
				FbPktCount:         0,
				PacketChunks: []rtcp.PacketStatusChunk{
					&rtcp.StatusVectorChunk{
						PacketStatusChunk: nil,
						Type:              rtcp.TypeTCCStatusVectorChunk,
						SymbolSize:        rtcp.TypeTCCSymbolSizeTwoBit,
						SymbolList: []uint16{
							rtcp.TypeTCCPacketReceivedSmallDelta,
							rtcp.TypeTCCPacketNotReceived,
							rtcp.TypeTCCPacketNotReceived,
							rtcp.TypeTCCPacketNotReceived,
							rtcp.TypeTCCPacketNotReceived,
							rtcp.TypeTCCPacketNotReceived,
							rtcp.TypeTCCPacketNotReceived,
						},
					},
				},
				RecvDeltas: []*rtcp.RecvDelta{
					{
						Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
						Delta: 4, // 4*250us=1ms
					},
				},
			})
			assertNoError(t, err)
		})
	})

	t.Run("worksOnSequenceNumberWrapAround", func(t *testing.T) {
		t0 := time.Time{}
		adapter := NewFeedbackAdapter()
		pkt65535 := getPacketWithTransportCCExt(t, 65535)
		pkt0 := getPacketWithTransportCCExt(t, 0)
		assertNoError(
			t,
			adapter.OnSent(t0, &pkt65535.Header, 1200, interceptor.Attributes{TwccExtensionAttributesKey: hdrExtID}),
		)
		assertNoError(
			t,
			adapter.OnSent(t0, &pkt0.Header, 1200, interceptor.Attributes{TwccExtensionAttributesKey: hdrExtID}),
		)

		results, err := adapter.OnTransportCCFeedback(t0, &rtcp.TransportLayerCC{
			Header:             rtcp.Header{},
			SenderSSRC:         0,
			MediaSSRC:          0,
			BaseSequenceNumber: 65535,
			PacketStatusCount:  2,
			ReferenceTime:      0,
			FbPktCount:         0,
			PacketChunks: []rtcp.PacketStatusChunk{
				&rtcp.StatusVectorChunk{
					PacketStatusChunk: nil,
					Type:              rtcp.TypeTCCStatusVectorChunk,
					SymbolSize:        rtcp.TypeTCCSymbolSizeTwoBit,
					SymbolList: []uint16{
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
					},
				},
			},
			RecvDeltas: []*rtcp.RecvDelta{
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
			},
		})
		assertNoError(t, err)

		assertNotEmpty(t, results)
		assertLen(t, results, 2) // the five statuses of packets never sent are left out
		assertContains(t, results, Acknowledgment{
			SequenceNumber: 65535,
			Size:           pkt65535.Header.MarshalSize() + 1200,
			Departure:      t0,
			Arrival:        t0.Add(4 * time.Microsecond),
		})
		assertContains(t, results, Acknowledgment{
			SequenceNumber: 0,
			Size:           pkt0.Header.MarshalSize() + 1200,
			Departure:      t0,
			Arrival:        t0.Add(8 * time.Microsecond),
		})
	})

	t.Run("ignoresPossiblyInFlightPackets", func(t *testing.T) {
		t0 := time.Time{}
		adapter := NewFeedbackAdapter()
		headers := []rtp.Header{}
		for i := range uint16(8) {
			pkt := getPacketWithTransportCCExt(t, i)
			headers = append(headers, pkt.Header)
			assertNoError(
				t,
				adapter.OnSent(t0, &pkt.Header, 1200, interceptor.Attributes{TwccExtensionAttributesKey: hdrExtID}),
			)
		}

		results, err := adapter.OnTransportCCFeedback(t0, &rtcp.TransportLayerCC{
			Header:             rtcp.Header{},
			SenderSSRC:         0,
			MediaSSRC:          0,
			BaseSequenceNumber: 0,
			PacketStatusCount:  3,
			ReferenceTime:      0,
			FbPktCount:         0,
			PacketChunks: []rtcp.PacketStatusChunk{
				&rtcp.StatusVectorChunk{
					PacketStatusChunk: nil,
					Type:              rtcp.TypeTCCStatusVectorChunk,
					SymbolSize:        rtcp.TypeTCCSymbolSizeTwoBit,
					SymbolList: []uint16{
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
					},
				},
			},
			RecvDeltas: []*rtcp.RecvDelta{
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4, // 4*250us=1ms
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4, // 4*250us=1ms
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4, // 4*250us=1ms
				},
			},
		})
		assertNoError(t, err)
		assertLen(t, results, 7)
		for i := range uint16(3) {
			assertContains(t, results, Acknowledgment{
				SequenceNumber: i,
				Size:           headers[i].MarshalSize() + 1200,
				Departure:      t0,
				Arrival:        t0.Add(time.Duration((i + 1)) * 4 * time.Microsecond),
			})
		}
		for i := uint16(3); i < 7; i++ {
			assertContains(t, results, Acknowledgment{
				SequenceNumber: i,
				Size:           headers[i].MarshalSize() + 1200,
				Departure:      t0,
				Arrival:        time.Time{},
			})
		}
	})

	t.Run("runLengthChunk", func(t *testing.T) {
		adapter := NewFeedbackAdapter()
		t0 := time.Time{}
		for i := range uint16(20) {
			pkt := getPacketWithTransportCCExt(t, i)
			assertNoError(
				t,
				adapter.OnSent(t0, &pkt.Header, 1200, interceptor.Attributes{TwccExtensionAttributesKey: hdrExtID}),
			)
		}
		packets, err := adapter.OnTransportCCFeedback(t0, &rtcp.TransportLayerCC{
			Header:             rtcp.Header{},
			SenderSSRC:         0,
			MediaSSRC:          0,
			BaseSequenceNumber: 0,
			PacketStatusCount:  3,
			ReferenceTime:      0,
			FbPktCount:         0,
			PacketChunks: []rtcp.PacketStatusChunk{
				&rtcp.RunLengthChunk{
					PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta,
					RunLength:          3,
				},
			},
			RecvDeltas: []*rtcp.RecvDelta{
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
			},
		})

		assertNoError(t, err)
		assertLen(t, packets, 3)
	})

	t.Run("statusVectorChunk", func(t *testing.T) {
		adapter := NewFeedbackAdapter()
		t0 := time.Time{}
		for i := range uint16(20) {
			pkt := getPacketWithTransportCCExt(t, i)
			assertNoError(
				t,
				adapter.OnSent(t0, &pkt.Header, 1200, interceptor.Attributes{TwccExtensionAttributesKey: hdrExtID}),
			)
		}
		packets, err := adapter.OnTransportCCFeedback(t0, &rtcp.TransportLayerCC{
			Header:             rtcp.Header{},
			SenderSSRC:         0,
			MediaSSRC:          0,
			BaseSequenceNumber: 0,
			PacketStatusCount:  3,
			ReferenceTime:      0,
			FbPktCount:         0,
			PacketChunks: []rtcp.PacketStatusChunk{
				&rtcp.StatusVectorChunk{
					SymbolSize: rtcp.TypeTCCSymbolSizeOneBit,
					SymbolList: []uint16{
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
					},
				},
			},
			RecvDeltas: []*rtcp.RecvDelta{
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
			},
		})

		assertNoError(t, err)
		assertLen(t, packets, 14)
	})

	t.Run("mixedRunLengthAndStatusVector", func(t *testing.T) {
		adapter := NewFeedbackAdapter()

		t0 := time.Time{}
		for i := range uint16(20) {
			pkt := getPacketWithTransportCCExt(t, i)
			assertNoError(
				t,
				adapter.OnSent(t0, &pkt.Header, 1200, interceptor.Attributes{TwccExtensionAttributesKey: hdrExtID}),
			)
		}

		packets, err := adapter.OnTransportCCFeedback(t0, &rtcp.TransportLayerCC{
			Header:             rtcp.Header{},
			SenderSSRC:         0,
			MediaSSRC:          0,
			BaseSequenceNumber: 0,
			PacketStatusCount:  10,
			ReferenceTime:      0,
			FbPktCount:         0,
			PacketChunks: []rtcp.PacketStatusChunk{
				&rtcp.StatusVectorChunk{
					SymbolSize: rtcp.TypeTCCSymbolSizeTwoBit,
					SymbolList: []uint16{
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketReceivedSmallDelta,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
						rtcp.TypeTCCPacketNotReceived,
					},
				},
				&rtcp.RunLengthChunk{
					PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta,
					RunLength:          3,
				},
			},
			RecvDeltas: []*rtcp.RecvDelta{
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
				{
					Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
					Delta: 4,
				},
			},
		})
		assertNoError(t, err)
		assertLen(t, packets, 10)
	})

	t.Run("doesNotcrashOnInvalidTWCCPacket", func(t *testing.T) {
		adapter := NewFeedbackAdapter()

		t0 := time.Time{}
		for i := uint16(1008); i < 1030; i++ {
			pkt := getPacketWithTransportCCExt(t, i)
			assertNoError(
				t,
				adapter.OnSent(t0, &pkt.Header, 1200, interceptor.Attributes{TwccExtensionAttributesKey: hdrExtID}),
			)
		}

		assertNotPanics(t, func() {
			packets, err := adapter.OnTransportCCFeedback(t0, &rtcp.TransportLayerCC{
				Header:             rtcp.Header{},
				SenderSSRC:         0,
				MediaSSRC:          0,
				BaseSequenceNumber: 1008,
				PacketStatusCount:  8,
				ReferenceTime:      278,
				FbPktCount:         170,
				PacketChunks: []rtcp.PacketStatusChunk{
					&rtcp.StatusVectorChunk{
						SymbolSize: rtcp.TypeTCCSymbolSizeTwoBit,
						SymbolList: []uint16{
							rtcp.TypeTCCPacketReceivedSmallDelta,
							rtcp.TypeTCCPacketReceivedSmallDelta,
							rtcp.TypeTCCPacketReceivedSmallDelta,
							rtcp.TypeTCCPacketReceivedSmallDelta,
							rtcp.TypeTCCPacketReceivedSmallDelta,
							rtcp.TypeTCCPacketReceivedSmallDelta,
							rtcp.TypeTCCPacketNotReceived,
						},
					},
					&rtcp.RunLengthChunk{
						PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta,
						RunLength:          5632,
					},
				},
				RecvDeltas: []*rtcp.RecvDelta{
					{
						Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
						Delta: 25000,
					},
					{
						Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
						Delta: 0,
					},
					{
						Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
						Delta: 29500,
					},
					{
						Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
						Delta: 16750,
					},
					{
						Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
						Delta: 23500,
					},
					{
						Type:  rtcp.TypeTCCPacketReceivedSmallDelta,
						Delta: 0,
					},
				},
			})
			assertError(t, err)
			assertEmpty(t, packets)
		})
	})
}

// libwebrtc reports a range again when a packet arrives late; every sent
// packet must count once, and packets the adapter never sent must not pass
// as lost nor shift the arrival times of those after them.
func TestFeedbackAdapterReportsEachPacketOnce(t *testing.T) {
	attributes := make(interceptor.Attributes)
	attributes.Set(TwccExtensionAttributesKey, hdrExtID)
	fa := NewFeedbackAdapter()
	for _, seq := range []uint16{10, 11, 12, 13} { // 9 was never sent through the adapter
		assertNoError(t, fa.OnSent(time.Time{}, &getPacketWithTransportCCExt(t, seq).Header, 0, attributes))
	}
	recv, lost := rtcp.TypeTCCPacketReceivedSmallDelta, rtcp.TypeTCCPacketNotReceived
	report := func(base uint16, symbols ...uint16) []Acknowledgment {
		var deltas []*rtcp.RecvDelta
		for _, s := range symbols {
			if s != lost {
				deltas = append(deltas, &rtcp.RecvDelta{Type: recv, Delta: 1000})
			}
		}
		acks, err := fa.OnTransportCCFeedback(time.Time{}, &rtcp.TransportLayerCC{
			BaseSequenceNumber: base, PacketStatusCount: uint16(len(symbols)),
			PacketChunks: []rtcp.PacketStatusChunk{&rtcp.StatusVectorChunk{
				SymbolSize: rtcp.TypeTCCSymbolSizeTwoBit, SymbolList: symbols}},
			RecvDeltas: deltas,
		})
		assertNoError(t, err)

		return acks
	}
	describe := func(acks []Acknowledgment) string {
		out := ""
		for _, a := range acks {
			if a.Arrival.IsZero() {
				out += fmt.Sprintf("%d:lost ", a.SequenceNumber)
			} else {
				out += fmt.Sprintf("%d:%dms ", a.SequenceNumber, a.Arrival.Sub(time.Time{}).Milliseconds())
			}
		}

		return out
	}
	// 9 (unknown) received, 10 received, 11 lost, 12 received.
	if got := describe(report(9, recv, recv, lost, recv)); got != "10:2ms 11:lost 12:3ms " {
		t.Fatalf("first report: %s", got)
	}
	// 11 arrives late: the range is reported again from 11 with 13.
	if got := describe(report(11, recv, recv, recv)); got != "13:3ms " {
		t.Fatalf("second report: %s, want only the packet not reported before", got)
	}
}

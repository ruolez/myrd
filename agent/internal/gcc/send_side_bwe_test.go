// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/twcc"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// mockTWCCResponder is a RTPWriter that writes
// TWCC feedback to a embedded SendSideBWE instantly.
type mockTWCCResponder struct {
	bwe     *SendSideBWE
	rtpChan chan []byte
}

func (m *mockTWCCResponder) Read(out []byte, _ interceptor.Attributes) (int, interceptor.Attributes, error) {
	pkt := <-m.rtpChan
	copy(out, pkt)

	return len(pkt), nil, nil
}

func (m *mockTWCCResponder) Write(pkts []rtcp.Packet, attributes interceptor.Attributes) (int, error) {
	return 0, m.bwe.WriteRTCP(pkts, attributes)
}

// mockGCCWriteStream receives RTP packets that have been paced by
// the congestion controller.
type mockGCCWriteStream struct {
	twccResponder *mockTWCCResponder
	t             *testing.T
}

func (m *mockGCCWriteStream) Write(header *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
	m.t.Helper()
	pkt, err := (&rtp.Packet{Header: *header, Payload: payload}).Marshal()
	assertNoError(m.t, err)

	m.twccResponder.rtpChan <- pkt

	return 0, err
}

// testPacer writes every packet straight through, gap apart.
type testPacer struct {
	gap     time.Duration
	mu      sync.Mutex
	writers map[uint32]interceptor.RTPWriter
}

func newTestPacer(gap time.Duration) *testPacer {
	return &testPacer{gap: gap, writers: map[uint32]interceptor.RTPWriter{}}
}

func (p *testPacer) Write(header *rtp.Header, payload []byte, attributes interceptor.Attributes) (int, error) {
	time.Sleep(p.gap)
	p.mu.Lock()
	w := p.writers[header.SSRC]
	p.mu.Unlock()

	return w.Write(header, payload, attributes)
}

func (p *testPacer) AddStream(ssrc uint32, writer interceptor.RTPWriter) {
	p.mu.Lock()
	p.writers[ssrc] = writer
	p.mu.Unlock()
}

func (p *testPacer) SetTargetBitrate(int) {}

func (p *testPacer) Close() error { return nil }

func TestSendSideBWE(t *testing.T) {
	buffer := make([]byte, 1500)
	rtpPayload := make([]byte, 1460)
	streamInfo := &interceptor.StreamInfo{
		SSRC:                1,
		RTPHeaderExtensions: []interceptor.RTPHeaderExtension{{URI: transportCCURI, ID: 1}},
	}

	bwe, err := NewSendSideBWE(
		WithLoggerFactory(logging.NewDefaultLoggerFactory()), SendSideBWEPacer(newTestPacer(5*time.Millisecond)),
	)
	requireNoError(t, err)
	requireNotNil(t, bwe)

	gccMock := &mockGCCWriteStream{
		twccResponder: &mockTWCCResponder{
			bwe,
			make(chan []byte, 500),
		},
		t: t,
	}

	twccSender, err := (&twcc.SenderInterceptorFactory{}).NewInterceptor("")
	requireNoError(t, err)
	requireNotNil(t, twccSender)

	twccInboundRTP := twccSender.BindRemoteStream(streamInfo, gccMock.twccResponder)
	twccSender.BindRTCPWriter(gccMock.twccResponder)

	requireEqual(t, latestBitrate, bwe.GetTargetBitrate())
	requireNotEqual(t, 0, len(bwe.GetStats()))

	rtpWriter := bwe.AddStream(streamInfo, gccMock)
	requireNotNil(t, rtpWriter)

	twccWriter := twcc.HeaderExtensionInterceptor{}
	rtpWriter = twccWriter.BindLocalStream(streamInfo, rtpWriter)

	for i := 0; i <= 100; i++ {
		_, err = rtpWriter.Write(&rtp.Header{SSRC: 1, Extensions: []rtp.Extension{}}, rtpPayload, nil)
		assertNoError(t, err)
		_, _, err = twccInboundRTP.Read(buffer, nil)
		assertNoError(t, err)
	}

	// Sending a stream with zero loss and no RTT should increase estimate
	requireLess(t, latestBitrate, bwe.GetTargetBitrate())
}

func TestSendSideBWE_RequiresPacer(t *testing.T) {
	_, err := NewSendSideBWE()
	requireErrorIs(t, err, ErrNoPacer)
}

func TestSendSideBWE_ErrorOnWriteRTCPAtClosedState(t *testing.T) {
	bwe, err := NewSendSideBWE(SendSideBWEPacer(newTestPacer(0)))
	requireNoError(t, err)
	requireNotNil(t, bwe)

	pkts := []rtcp.Packet{&rtcp.TransportLayerCC{}}
	requireNoError(t, bwe.WriteRTCP(pkts, nil))
	requireEqual(t, bwe.isClosed(), false)
	requireNoError(t, bwe.Close())
	requireErrorIs(t, bwe.WriteRTCP(pkts, nil), ErrSendSideBWEClosed)
	requireEqual(t, bwe.isClosed(), true)
}

func BenchmarkSendSideBWE_WriteRTCP(b *testing.B) {
	numSequencesPerTwccReport := []int{10, 100, 500, 1000}

	for _, count := range numSequencesPerTwccReport {
		b.Run(fmt.Sprintf("num_sequences=%d", count), func(b *testing.B) {
			bwe, err := NewSendSideBWE(SendSideBWEPacer(newTestPacer(0)))
			requireNoError(b, err)
			requireNotNil(b, bwe)

			recorder := twcc.NewRecorder(5000)
			seq := uint16(0)
			arrivalTime := int64(0)

			for i := 0; i < b.N; i++ {
				seqs := rand.Intn(count/2) + count // [count, count * 1.5)
				for range seqs {
					seq++

					if rand.Intn(5) != 0 {
						arrivalTime += int64(rtcp.TypeTCCDeltaScaleFactor * (rand.Intn(128) + 1))
						recorder.Record(5000, seq, arrivalTime)
					}
				}

				rtcpPackets := recorder.BuildFeedbackPacket()
				requireEqual(b, 1, len(rtcpPackets))

				requireNoError(b, bwe.WriteRTCP(rtcpPackets, nil))
			}

			requireNoError(b, bwe.Close())
		})
	}
}

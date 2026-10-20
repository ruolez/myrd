package app

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

type metrics struct {
	reg           *prometheus.Registry
	sessionsTotal *prometheus.CounterVec
	reconnects    prometheus.Counter
	signalLatency prometheus.Histogram
}

func newMetrics(h *Hub, pool *pgxpool.Pool) *metrics {
	reg := prometheus.NewRegistry()
	m := &metrics{
		reg: reg,
		sessionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "remotectl_sessions_total", Help: "Sessions closed, by ICE path and result.",
		}, []string{"path", "result"}),
		reconnects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "remotectl_ws_reconnects_total", Help: "Agent WebSocket reconnects (connects after the first per device since boot).",
		}),
		signalLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "remotectl_signaling_latency_seconds", Help: "Time to handle and forward one signaling message.",
			Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 1},
		}),
	}
	reg.MustRegister(m.sessionsTotal, m.reconnects, m.signalLatency,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "remotectl_agents_online", Help: "Agents with an open WebSocket."},
			func() float64 { a, _ := h.Counts(); return float64(a) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "remotectl_browsers_connected", Help: "Browser WebSockets."},
			func() float64 { _, b := h.Counts(); return float64(b) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "remotectl_sessions_active", Help: "Sessions in state active."},
			func() float64 { return float64(h.ActiveCount()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "remotectl_db_pool_conns", Help: "Open Postgres connections in the pool."},
			func() float64 { return float64(pool.Stat().TotalConns()) }),
	)
	return m
}

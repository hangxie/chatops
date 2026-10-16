// Package metrics exports question, turn, and MCP metrics in the Prometheus format, plus liveness and readiness probes.
package metrics

import (
	"net/http"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hangxie/chatops/internal/chat"
	"github.com/hangxie/chatops/internal/mcp"
)

// Metrics owns a dedicated registry; it implements conversation.Observer.
type Metrics struct {
	registry *prometheus.Registry
	sources  atomic.Pointer[Sources] // nil until Watch, so readiness reports starting

	received      *prometheus.CounterVec
	rejected      *prometheus.CounterVec
	replyFailures *prometheus.CounterVec
	response      *prometheus.HistogramVec
	queueWait     *prometheus.HistogramVec

	iterations prometheus.Histogram
	modelCall  *prometheus.HistogramVec
	tokens     *prometheus.CounterVec
	toolCall   *prometheus.HistogramVec
	toolBytes  *prometheus.HistogramVec
	truncated  *prometheus.CounterVec
}

// Sources are read at scrape and probe time.
type Sources struct {
	Pending   func() int
	InFlight  func() int
	Accepting func() bool
	Chat      func() []chat.BackendHealth
	MCP       func() []mcp.ServerHealth
}

// New registers every collector, plus Go runtime, process, and build info metrics.
func New(version string) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),

		received:      counter("chatops_questions_received_total", "Questions received, including rejected ones.", "backend"),
		rejected:      counter("chatops_questions_rejected_total", "Questions refused before running.", "backend", "reason"),
		replyFailures: counter("chatops_reply_failures_total", "Replies that could not be posted.", "backend"),
		response: histogram("chatops_response_duration_seconds", "Time from receipt to a delivered reply.",
			[]float64{0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300}, "backend", "outcome"),
		queueWait: histogram("chatops_queue_wait_seconds", "Time a question waited behind its conversation and for a turn slot.",
			[]float64{0.01, 0.1, 0.5, 1, 5, 10, 30, 60, 120}, "backend"),

		iterations: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "chatops_turn_iterations", Help: "Model calls per turn.",
			Buckets: []float64{1, 2, 3, 4, 5, 6, 8, 10, 15, 20},
		}),
		modelCall: histogram("chatops_model_call_duration_seconds", "Model call latency.",
			[]float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 60, 120}, "phase", "outcome"),
		tokens: counter("chatops_model_tokens_total", "Tokens reported by the model endpoint.", "phase", "kind"),
		toolCall: histogram("chatops_tool_call_duration_seconds", "Tool call latency, from the model's request to the result.",
			[]float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30}, "server", "tool", "outcome"),
		toolBytes: histogram("chatops_tool_result_bytes", "Tool result size before truncation.",
			prometheus.ExponentialBuckets(256, 4, 8), "server", "tool"),
		truncated: counter("chatops_tool_result_truncated_total", "Tool results cut to max_tool_result_bytes.", "server", "tool"),
	}
	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "chatops_build_info", Help: "Always 1; labeled with the running version.",
		ConstLabels: prometheus.Labels{"version": version},
	})
	buildInfo.Set(1)
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		buildInfo,
		m.received, m.rejected, m.replyFailures, m.response, m.queueWait,
		m.iterations, m.modelCall, m.tokens, m.toolCall, m.toolBytes, m.truncated,
	)
	return m
}

// Watch registers gauges read from s; call it once, after the sources exist.
func (m *Metrics) Watch(s Sources) {
	m.registry.MustRegister(
		gaugeFunc("chatops_questions_pending", "Accepted questions not yet answered.", s.Pending),
		gaugeFunc("chatops_turns_in_flight", "Turns holding a slot.", s.InFlight),
		chatCollector(s.Chat),
		mcpCollector(s.MCP),
	)
	m.sources.Store(&s)
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

func counter(name, help string, labels ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
}

func histogram(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: buckets}, labels)
}

func gaugeFunc(name, help string, read func() int) prometheus.GaugeFunc {
	return prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help}, func() float64 { return float64(read()) })
}

var backendUpDesc = prometheus.NewDesc("chatops_chat_backend_up", "1 while the chat backend can receive messages.", []string{"backend"}, nil)

// chatCollector reports chat backend health at scrape time.
type chatCollector func() []chat.BackendHealth

func (c chatCollector) Describe(ch chan<- *prometheus.Desc) { ch <- backendUpDesc }

func (c chatCollector) Collect(ch chan<- prometheus.Metric) {
	for _, backend := range c() {
		ch <- prometheus.MustNewConstMetric(backendUpDesc, prometheus.GaugeValue, gaugeBool(backend.Up), backend.Name)
	}
}

var (
	serverUpDesc = prometheus.NewDesc("chatops_mcp_server_up", "1 while the MCP server's session is up.", []string{"server"}, nil)
	toolsDesc    = prometheus.NewDesc("chatops_mcp_tools", "Tools the catalog holds from the MCP server.", []string{"server"}, nil)
)

// mcpCollector reports MCP server health at scrape time.
type mcpCollector func() []mcp.ServerHealth

func (c mcpCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- serverUpDesc
	ch <- toolsDesc
}

func (c mcpCollector) Collect(ch chan<- prometheus.Metric) {
	for _, server := range c() {
		ch <- prometheus.MustNewConstMetric(serverUpDesc, prometheus.GaugeValue, gaugeBool(server.Up), server.ID)
		ch <- prometheus.MustNewConstMetric(toolsDesc, prometheus.GaugeValue, float64(server.Tools), server.ID)
	}
}

func gaugeBool(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

package app

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/loynet/ptchan-ai/deepseek"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type metrics struct {
	ready         *atomic.Bool
	telegramReady prometheus.Gauge
	reads         *prometheus.CounterVec
	registry      *prometheus.Registry
	operation     *prometheus.CounterVec
	admissions    *prometheus.CounterVec
	replies       *prometheus.CounterVec
	context       *prometheus.CounterVec
	model         *prometheus.CounterVec
	tokens        *prometheus.CounterVec
	active        *prometheus.GaugeVec
}

func newMetrics() *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		telegramReady: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tessa_telegram_worker_ready",
			Help: "Whether the Telegram worker has successfully polled and is not in an outage.",
		}),
		reads: counter("tessa_telegram_read_requests_total", "Telegram read attempts by method and bounded outcome.", "method", "result"),
		operation: counter(
			"tessa_telegram_poll_requests_total",
			"Telegram polling requests.",
			"result",
		),
		admissions: counter(
			"tessa_chat_admissions_total",
			"Chat admission decisions.",
			"result",
		),
		replies: counter(
			"tessa_chat_reply_deliveries_total",
			"Chat reply deliveries.",
			"result",
		),
		context: counter(
			"tessa_chat_context_uses_total",
			"Chat context uses.",
			"type",
		),
		model: counter(
			"tessa_model_completions_total",
			"Model completions.",
			"provider", "model", "outcome",
		),
		tokens: counter(
			"tessa_model_tokens_total",
			"Model tokens.",
			"provider", "model", "type",
		),
		active: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "tessa_chat_active_conversations",
				Help: "Active chat conversations.",
			},
			nil,
		),
	}
	m.registry.MustRegister(m.telegramReady, m.reads, m.operation, m.admissions, m.replies, m.context, m.model, m.tokens, m.active)
	return m
}

func counter(n, h string, l ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: n, Help: h}, l)
}

func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *metrics) ObserveTelegramPoll(_ time.Duration, e error) {
	r := "success"
	if e != nil {
		r = "error"
	}
	m.operation.WithLabelValues(r).Inc()
}

func (m *metrics) ObserveAdmission(r string) { m.admissions.WithLabelValues(r).Inc() }
func (m *metrics) ObserveReply(r string)     { m.replies.WithLabelValues(r).Inc() }
func (m *metrics) ObserveContext(t string)   { m.context.WithLabelValues(t).Inc() }
func (m *metrics) SetActiveConversations(n int) {
	m.active.WithLabelValues().Set(float64(n))
}

func (m *metrics) ObserveModelCompletion(p, model string, _ time.Duration, c deepseek.Completion, e error) {
	o := string(c.FinishReason)
	if e != nil {
		o = "error"
	}
	if o == "" {
		o = "unknown"
	}
	m.model.WithLabelValues(p, model, o).Inc()
	if e == nil {
		m.tokens.WithLabelValues(p, model, "input_cache_hit").Add(float64(c.Usage.PromptCacheHitTokens))
		m.tokens.WithLabelValues(p, model, "input_cache_miss").Add(float64(c.Usage.PromptCacheMissTokens))
		m.tokens.WithLabelValues(p, model, "output").Add(float64(c.Usage.CompletionTokens))
	}
}

func (m *metrics) ObserveTelegramRead(method, result string) {
	m.reads.WithLabelValues(method, result).Inc()
}

func (m *metrics) SetTelegramReady(ready bool) {
	if m.ready != nil {
		m.ready.Store(ready)
	}
	value := 0.0
	if ready {
		value = 1
	}
	m.telegramReady.Set(value)
}

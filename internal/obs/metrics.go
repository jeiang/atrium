package obs

import (
	"slices"
	"strconv"
	"strings"
	"sync"
)

type Sample struct {
	Name   string
	Labels [][2]string
	Value  float64
}

type Metric struct {
	Name     string
	Help     string
	Type     string
	Snapshot func() []Sample
}

type Counter struct {
	name   string
	help   string
	labels []string
	mu     sync.Mutex
	series map[string]*counterSeries
}

type counterSeries struct {
	values []string
	value  float64
}

func NewCounter(name, help string, labels ...string) *Counter {
	return &Counter{name: name, help: help, labels: labels, series: map[string]*counterSeries{}}
}

func (c *Counter) Add(delta float64, values ...string) {
	if len(values) != len(c.labels) {
		return
	}
	key := strings.Join(values, "\x00")
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.series[key]
	if !ok {
		s = &counterSeries{values: slices.Clone(values)}
		c.series[key] = s
	}
	s.value += delta
}

func (c *Counter) Snapshot() []Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.series))
	for key := range c.series {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make([]Sample, 0, len(keys))
	for _, key := range keys {
		s := c.series[key]
		out = append(out, Sample{Labels: pairs(c.labels, s.values), Value: s.value})
	}
	return out
}

type Histogram struct {
	name    string
	help    string
	labels  []string
	buckets []float64
	mu      sync.Mutex
	series  map[string]*histSeries
}

type histSeries struct {
	values []string
	counts []uint64
	sum    float64
	count  uint64
}

func NewHistogram(name, help string, buckets []float64, labels ...string) *Histogram {
	return &Histogram{name: name, help: help, labels: labels, buckets: buckets, series: map[string]*histSeries{}}
}

func (h *Histogram) Observe(value float64, values ...string) {
	if len(values) != len(h.labels) {
		return
	}
	key := strings.Join(values, "\x00")
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.series[key]
	if !ok {
		s = &histSeries{values: slices.Clone(values), counts: make([]uint64, len(h.buckets))}
		h.series[key] = s
	}
	for i, bound := range h.buckets {
		if value <= bound {
			s.counts[i]++
		}
	}
	s.count++
	s.sum += value
}

func (h *Histogram) Snapshot() []Sample {
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := make([]string, 0, len(h.series))
	for key := range h.series {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make([]Sample, 0, len(keys)*(len(h.buckets)+3))
	for _, key := range keys {
		s := h.series[key]
		base := pairs(h.labels, s.values)
		for i, bound := range h.buckets {
			out = append(out, Sample{Name: h.name + "_bucket", Labels: withPair(base, "le", formatBound(bound)), Value: float64(s.counts[i])})
		}
		out = append(out, Sample{Name: h.name + "_bucket", Labels: withPair(base, "le", "+Inf"), Value: float64(s.count)})
		out = append(out, Sample{Name: h.name + "_sum", Labels: base, Value: s.sum})
		out = append(out, Sample{Name: h.name + "_count", Labels: base, Value: float64(s.count)})
	}
	return out
}

func pairs(labels, values []string) [][2]string {
	out := make([][2]string, len(labels))
	for i := range labels {
		out[i] = [2]string{labels[i], values[i]}
	}
	return out
}

func withPair(base [][2]string, key, value string) [][2]string {
	out := make([][2]string, len(base), len(base)+1)
	copy(out, base)
	return append(out, [2]string{key, value})
}

func formatBound(value float64) string { return strconv.FormatFloat(value, 'g', -1, 64) }

var (
	httpBuckets  = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	llmBuckets   = []float64{0.5, 1, 2.5, 5, 10, 20, 30, 60, 120, 300, 600}
	runBuckets   = []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800, 3600}
	stepBuckets  = []float64{1, 2, 4, 8, 12, 16, 24, 32, 48, 64}
	toolBuckets  = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
	mailBuckets  = []float64{1, 2.5, 5, 10, 30, 60, 120, 300, 600}
	embedBuckets = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
	dbBuckets    = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}
)

var (
	HTTPRequests = NewCounter("atrium_http_requests_total", "HTTP requests served, by route, method and status.", "route", "method", "status")
	HTTPDuration = NewHistogram("atrium_http_request_duration_seconds", "HTTP request duration in seconds.", httpBuckets, "route", "method", "status")

	LLMRequests = NewCounter("atrium_llm_requests_total", "Anthropic requests, by model, run kind and whether the response was partial.", "model", "kind", "partial")
	LLMDuration = NewHistogram("atrium_llm_request_duration_seconds", "Anthropic request duration in seconds.", llmBuckets, "model", "kind")
	LLMTokens   = NewCounter("atrium_llm_tokens_total", "Anthropic tokens, by model, run kind and token type.", "model", "kind", "type")
	LLMCost     = NewCounter("atrium_llm_cost_usd_total", "Anthropic cost in US dollars, by model and run kind.", "model", "kind")

	Runs        = NewCounter("atrium_runs_total", "Agent runs, by run kind and final state.", "kind", "state")
	RunSteps    = NewHistogram("atrium_run_steps", "Steps taken by a finished agent run.", stepBuckets, "kind")
	RunDuration = NewHistogram("atrium_run_duration_seconds", "Agent run duration in seconds.", runBuckets, "kind")

	ToolCalls    = NewCounter("atrium_tool_calls_total", "Tool calls, by tool name and outcome.", "tool", "ok")
	ToolDuration = NewHistogram("atrium_tool_duration_seconds", "Tool call duration in seconds.", toolBuckets, "tool", "ok")

	MailNew         = NewCounter("atrium_mail_sync_new_total", "New messages seen by mail sync, by account.", "account")
	MailGone        = NewCounter("atrium_mail_sync_gone_total", "Messages that disappeared during mail sync, by account.", "account")
	MailFlagChanges = NewCounter("atrium_mail_sync_flag_changes_total", "Flag changes applied by mail sync, by account.", "account")
	MailDuration    = NewHistogram("atrium_mail_sync_duration_seconds", "Mail sync duration in seconds, by account.", mailBuckets, "account")

	EmbedRequests = NewCounter("atrium_embed_requests_total", "Embedding requests, by model and outcome.", "model", "ok")
	EmbedDuration = NewHistogram("atrium_embed_duration_seconds", "Embedding request duration in seconds.", embedBuckets, "model")

	NotifyDelivery = NewCounter("atrium_notification_delivery_total", "Notification delivery attempts, by channel, backend and outcome.", "channel", "backend", "ok")

	DBTransactions = NewCounter("atrium_db_transactions_total", "Database transactions, by outcome.", "ok")
	DBDuration     = NewHistogram("atrium_db_transaction_duration_seconds", "Database transaction duration in seconds.", dbBuckets)
)

func Metrics() []Metric {
	return []Metric{
		{Name: HTTPRequests.name, Help: HTTPRequests.help, Type: "counter", Snapshot: HTTPRequests.Snapshot},
		{Name: HTTPDuration.name, Help: HTTPDuration.help, Type: "histogram", Snapshot: HTTPDuration.Snapshot},
		{Name: LLMRequests.name, Help: LLMRequests.help, Type: "counter", Snapshot: LLMRequests.Snapshot},
		{Name: LLMDuration.name, Help: LLMDuration.help, Type: "histogram", Snapshot: LLMDuration.Snapshot},
		{Name: LLMTokens.name, Help: LLMTokens.help, Type: "counter", Snapshot: LLMTokens.Snapshot},
		{Name: LLMCost.name, Help: LLMCost.help, Type: "counter", Snapshot: LLMCost.Snapshot},
		{Name: Runs.name, Help: Runs.help, Type: "counter", Snapshot: Runs.Snapshot},
		{Name: RunSteps.name, Help: RunSteps.help, Type: "histogram", Snapshot: RunSteps.Snapshot},
		{Name: RunDuration.name, Help: RunDuration.help, Type: "histogram", Snapshot: RunDuration.Snapshot},
		{Name: ToolCalls.name, Help: ToolCalls.help, Type: "counter", Snapshot: ToolCalls.Snapshot},
		{Name: ToolDuration.name, Help: ToolDuration.help, Type: "histogram", Snapshot: ToolDuration.Snapshot},
		{Name: MailNew.name, Help: MailNew.help, Type: "counter", Snapshot: MailNew.Snapshot},
		{Name: MailGone.name, Help: MailGone.help, Type: "counter", Snapshot: MailGone.Snapshot},
		{Name: MailFlagChanges.name, Help: MailFlagChanges.help, Type: "counter", Snapshot: MailFlagChanges.Snapshot},
		{Name: MailDuration.name, Help: MailDuration.help, Type: "histogram", Snapshot: MailDuration.Snapshot},
		{Name: EmbedRequests.name, Help: EmbedRequests.help, Type: "counter", Snapshot: EmbedRequests.Snapshot},
		{Name: EmbedDuration.name, Help: EmbedDuration.help, Type: "histogram", Snapshot: EmbedDuration.Snapshot},
		{Name: NotifyDelivery.name, Help: NotifyDelivery.help, Type: "counter", Snapshot: NotifyDelivery.Snapshot},
		{Name: DBTransactions.name, Help: DBTransactions.help, Type: "counter", Snapshot: DBTransactions.Snapshot},
		{Name: DBDuration.name, Help: DBDuration.help, Type: "histogram", Snapshot: DBDuration.Snapshot},
	}
}

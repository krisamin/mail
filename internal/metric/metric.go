// Package metric is the process-wide Prometheus instrumentation.
//
// One package owns every metric so names stay consistent (mail_ prefix,
// snake_case, unit suffixes) and callers just import and increment.
// Exposed via promhttp on the internal metrics listener (MAIL_METRIC_ADDR,
// cluster-internal only — never routed through the gateway).
package metric

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ── delivery pipeline ───────────────────────────────────────

// DeliveryTotal counts local deliveries by entry point and outcome.
// origin: smtp | submission | webmail | queue
// outcome: delivered | discarded | quota | error
var DeliveryTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "mail_delivery_total",
	Help: "Local deliveries by origin and outcome.",
}, []string{"origin", "outcome"})

// ── SMTP inbound screening ──────────────────────────────────

// InboundRejectTotal counts inbound SMTP rejections by reason.
// reason: dnsbl | greylist | dmarc | rspamd | unknown_user | spf
var InboundRejectTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "mail_inbound_reject_total",
	Help: "Inbound SMTP rejections by reason.",
}, []string{"reason"})

// QuarantineTotal counts messages delivered to Junk by trigger.
// trigger: dmarc | screening | rspamd
var QuarantineTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "mail_quarantine_total",
	Help: "Messages quarantined to Junk by trigger.",
}, []string{"trigger"})

// ── authentication ──────────────────────────────────────────

// AuthTotal counts app-password authentication attempts.
// protocol: imap | submission; outcome: ok | fail | blocked
var AuthTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "mail_auth_total",
	Help: "App-password authentication attempts by protocol and outcome.",
}, []string{"protocol", "outcome"})

// ── outbound queue ──────────────────────────────────────────

// QueueSendTotal counts outbound queue processing outcomes.
// outcome: sent | retry | failed
var QueueSendTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "mail_queue_send_total",
	Help: "Outbound queue processing outcomes.",
}, []string{"outcome"})

// QueuePendingGauge is the current number of pending outbound items
// (refreshed on each worker poll).
var QueuePendingGauge = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "mail_queue_pending",
	Help: "Pending outbound queue items (sampled each worker poll).",
})

// SendDuration measures per-message relay/MX send time.
var SendDuration = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "mail_send_duration_seconds",
	Help:    "Outbound per-message send duration.",
	Buckets: prometheus.DefBuckets,
})

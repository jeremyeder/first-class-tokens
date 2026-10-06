package controller

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// The intake metrics deliberately use only bounded, policy-shaped labels.
// BusinessWorkItem names, stable IDs, Job names, and model-provided free text
// must never become Prometheus label values.
var (
	intakeDecisionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "first_class_tokens_intake_decisions_total",
			Help: "Validated model decisions observed by the intake controller.",
		},
		[]string{"result", "reason", "fare_class"},
	)
	intakeDecisionConfidence = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "first_class_tokens_intake_decision_confidence",
			Help:    "Confidence values returned by the decision model before projection.",
			Buckets: []float64{0, 0.5, 0.8, 0.9, 0.95, 0.99, 1},
		},
		[]string{"fare_class"},
	)
	intakeDecisionDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "first_class_tokens_intake_decision_duration_seconds",
			Help:    "Time spent waiting for the direct decision provider.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
		},
		[]string{"result"},
	)
	intakeDecisionCacheHits = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "first_class_tokens_intake_decision_cache_hits_total",
			Help: "Reconciliations that reused the current generation's recorded decision.",
		},
	)
	intakeProjectionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "first_class_tokens_intake_projections_total",
			Help: "Queue projection outcomes produced by the intake controller.",
		},
		[]string{"outcome", "reason", "fare_class"},
	)
)

func init() {
	crmetrics.Registry.MustRegister(
		intakeDecisionsTotal,
		intakeDecisionConfidence,
		intakeDecisionDuration,
		intakeDecisionCacheHits,
		intakeProjectionsTotal,
	)
}

func observeDecision(result, reason string, fareClass string, confidence float64) {
	class := metricFareClass(fareClass)
	intakeDecisionsTotal.WithLabelValues(result, reason, class).Inc()
	if (strings.TrimSpace(fareClass) != "" || confidence != 0) && confidence >= 0 && confidence <= 1 {
		intakeDecisionConfidence.WithLabelValues(class).Observe(confidence)
	}
}

func observeDecisionDuration(result string, seconds float64) {
	if seconds >= 0 {
		intakeDecisionDuration.WithLabelValues(result).Observe(seconds)
	}
}

func observeProjection(outcome, reason, fareClass string) {
	intakeProjectionsTotal.WithLabelValues(outcome, reason, metricFareClass(fareClass)).Inc()
}

func metricFareClass(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "first", "first class", "first-class", "fare-first-class":
		return "first"
	case "business", "fare-business":
		return "business"
	case "economy", "fare-economy":
		return "economy"
	default:
		return "unknown"
	}
}

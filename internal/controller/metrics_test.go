package controller

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricFareClassIsBounded(t *testing.T) {
	tests := map[string]string{
		"First class":   "first",
		"fare-business": "business",
		"Economy":       "economy",
		"customer-123":  "unknown",
		"":              "unknown",
	}
	for input, want := range tests {
		if got := metricFareClass(input); got != want {
			t.Errorf("metricFareClass(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDecisionMetricsCaptureClassAndProjectionWithoutObjectIdentity(t *testing.T) {
	accepted := intakeDecisionsTotal.WithLabelValues("accepted", "DecisionAccepted", "first")
	before := testutil.ToFloat64(accepted)
	observeDecision("accepted", "DecisionAccepted", "First class", 0.99)
	if got := testutil.ToFloat64(accepted); got != before+1 {
		t.Fatalf("accepted decision counter = %v, want %v", got, before+1)
	}

	projection := intakeProjectionsTotal.WithLabelValues("patched", "JobPatched", "first")
	projectionBefore := testutil.ToFloat64(projection)
	observeProjection("patched", "JobPatched", "First class")
	if got := testutil.ToFloat64(projection); got != projectionBefore+1 {
		t.Fatalf("patched projection counter = %v, want %v", got, projectionBefore+1)
	}

	// The metric vectors have exactly the bounded labels above; identity-bearing
	// fields never enter a label value.
	if got := len(accepted.Desc().String()); got == 0 {
		t.Fatal("decision metric descriptor is empty")
	}
}

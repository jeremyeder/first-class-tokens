package evaluation

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	api "github.com/jeder/first-class-tokens/api/v1alpha1"
	"github.com/jeder/first-class-tokens/internal/decision"
	"github.com/jeder/first-class-tokens/internal/policy"
)

func TestLoadSuiteAndAllCheckedInFixtureCases(t *testing.T) {
	root := filepath.Join("..", "..")
	suiteData, err := os.ReadFile(filepath.Join(root, "config", "evaluation", "expected-outcomes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	suite, err := LoadSuite(suiteData)
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}
	workItems, err := os.ReadFile(filepath.Join(root, "config", "fixtures", "business-work-items.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := os.ReadFile(filepath.Join(root, "config", "fixtures", "business-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cases, err := LoadFixtureCases(workItems, events, suite)
	if err != nil {
		t.Fatalf("LoadFixtureCases() error = %v", err)
	}
	if len(cases) != 23 {
		t.Fatalf("loaded %d cases, want 23 (20 initial events plus 3 updates)", len(cases))
	}
	if cases[0].Expected != policy.FareFirstClass || cases[2].Expected != policy.FareBusiness {
		t.Fatalf("unexpected independent expected outcomes: first=%q third=%q", cases[0].Expected, cases[2].Expected)
	}
	if cases[20].EventID != "synthetic-crm-001-v2" || cases[20].Expected != policy.FareEconomy {
		t.Fatalf("unexpected churn outcome: %+v", cases[20])
	}
	if cases[0].Facts.SourceFacts["renewal_value_usd"] != "850000" {
		t.Fatalf("source facts were not loaded: %#v", cases[0].Facts.SourceFacts)
	}
}

func TestLoadFixtureCasesRequiresExpectedCoverage(t *testing.T) {
	suite := Suite{
		Name:       "coverage",
		Repeats:    1,
		Thresholds: Thresholds{MinimumExactMatchAccuracy: 0, MinimumItemStability: 0, MaximumP95LatencyMs: 1},
		Outcomes:   []ExpectedOutcome{{WorkItemID: "work-item-001", EventID: "event-001", ExpectedFareClass: policy.FareFirstClass}},
	}
	workItems := []byte("items:\n- id: work-item-001\n  source: crm\n  sourceRef: source-001\n  provenance: synthetic\n  title: Example\n")
	events := []byte(`{"event_id":"event-001","occurred_at":"2026-10-01T18:00:00Z","work_item_id":"work-item-001","source":"crm","provenance":"synthetic","facts":{"severity":"high"}}` + "\n")
	if _, err := LoadFixtureCases(workItems, events, suite); err != nil {
		t.Fatalf("LoadFixtureCases() error = %v", err)
	}
	suite.Outcomes = nil
	if _, err := LoadFixtureCases(workItems, events, suite); err == nil {
		t.Fatal("LoadFixtureCases() error = nil, want missing expected outcome")
	}
}

func TestEvaluateReportsAccuracyStabilityFlipsAndConfidence(t *testing.T) {
	cases := []InputCase{
		{WorkItemID: "work-item-001", EventID: "event-001", Expected: policy.FareFirstClass, Facts: api.BusinessFacts{StableID: "work-item-001", SourceFacts: map[string]string{"severity": "critical"}}},
		{WorkItemID: "work-item-002", EventID: "event-002", Expected: policy.FareEconomy, Facts: api.BusinessFacts{StableID: "work-item-002", SourceFacts: map[string]string{"severity": "low"}}},
	}
	client := &scriptedClient{responses: []scriptedResponse{
		{fareClass: "first", confidence: 0.91},
		{fareClass: "first", confidence: 0.92},
		{fareClass: "first", confidence: 0.93},
		{fareClass: "first", confidence: 0.94},
		{fareClass: "first", confidence: 0.95},
		{fareClass: "economy", confidence: 0.81},
		{fareClass: "economy", confidence: 0.82},
		{fareClass: "business", confidence: 0.83},
		{fareClass: "economy", confidence: 0.84},
		{fareClass: "economy", confidence: 0.85},
	}}
	report, err := (Runner{Client: client, Policy: policy.Default(), Repeats: 5}).Evaluate(context.Background(), "test", Thresholds{
		MinimumExactMatchAccuracy: 0.90,
		MinimumItemStability:      0.80,
		MaximumP95LatencyMs:       5000,
	}, cases)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !report.Passed || report.Summary.ExactMatchAccuracy != 0.9 || report.Summary.MinimumItemStability != 0.8 {
		t.Fatalf("unexpected passing report: %+v", report)
	}
	if report.Cases[1].ClassFlips != 2 || report.Cases[1].MajorityFareClass != policy.FareEconomy || len(report.Cases[1].DistinctFareClasses) != 2 {
		t.Fatalf("flip summary = %+v", report.Cases[1])
	}
	if report.Cases[0].Predictions[0].Confidence != 0.91 {
		t.Fatalf("confidence was not retained: %+v", report.Cases[0].Predictions[0])
	}
	if len(client.requests) != 10 {
		t.Fatalf("got %d requests, want 10", len(client.requests))
	}
	for _, request := range client.requests {
		if request.Facts.SourceFacts["expectedFareClass"] != "" {
			t.Fatal("expected outcome leaked into model facts")
		}
	}
}

func TestEvaluateFailsWhenMinimumStabilityIsNotMet(t *testing.T) {
	client := &scriptedClient{responses: []scriptedResponse{
		{fareClass: "first", confidence: 0.9},
		{fareClass: "business", confidence: 0.9},
		{fareClass: "first", confidence: 0.9},
		{fareClass: "business", confidence: 0.9},
		{fareClass: "first", confidence: 0.9},
	}}
	report, err := (Runner{Client: client, Policy: policy.Default(), Repeats: 5}).Evaluate(context.Background(), "test", Thresholds{
		MinimumExactMatchAccuracy: 0,
		MinimumItemStability:      0.80,
		MaximumP95LatencyMs:       5000,
	}, []InputCase{{WorkItemID: "work-item-001", EventID: "event-001", Expected: policy.FareFirstClass}})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.Passed {
		t.Fatalf("Evaluate() passed unstable report: %+v", report)
	}
	if report.Summary.MinimumItemStability != 0.6 || report.Cases[0].ClassFlips != 4 {
		t.Fatalf("unexpected instability metrics: %+v", report.Cases[0])
	}
}

type scriptedResponse struct {
	fareClass  string
	confidence float64
	err        error
}

type scriptedClient struct {
	responses []scriptedResponse
	requests  []decision.DecisionRequest
	index     int
}

func (c *scriptedClient) Decide(_ context.Context, request decision.DecisionRequest) (decision.Decision, error) {
	c.requests = append(c.requests, request)
	if c.index >= len(c.responses) {
		return decision.Decision{}, context.Canceled
	}
	response := c.responses[c.index]
	c.index++
	if response.err != nil {
		return decision.Decision{}, response.err
	}
	return decision.Decision{FareClass: response.fareClass, Confidence: response.confidence, Provider: "fake"}, nil
}

func TestPercentileInterpolatesSortedValues(t *testing.T) {
	if got := percentile([]float64{4, 1, 3, 2, 5}, 0.95); got != 4.8 {
		t.Fatalf("percentile() = %v, want 4.8", got)
	}
}

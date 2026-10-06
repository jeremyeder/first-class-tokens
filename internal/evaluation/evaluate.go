package evaluation

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jeder/first-class-tokens/internal/decision"
	"github.com/jeder/first-class-tokens/internal/policy"
)

// Prediction is one observed model call. Error is populated when the model
// call failed; failed calls count against the quality gate and are retained in
// the JSON report for diagnosis.
type Prediction struct {
	Attempt    int     `json:"attempt"`
	FareClass  string  `json:"fareClass,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Provider   string  `json:"provider,omitempty"`
	LatencyMs  float64 `json:"latencyMs"`
	Error      string  `json:"error,omitempty"`
}

// CaseReport contains the repeated observations for one event snapshot.
type CaseReport struct {
	WorkItemID          string       `json:"workItemID"`
	EventID             string       `json:"eventID"`
	OccurredAt          time.Time    `json:"occurredAt"`
	ExpectedFareClass   string       `json:"expectedFareClass"`
	Rationale           string       `json:"rationale,omitempty"`
	Predictions         []Prediction `json:"predictions"`
	MajorityFareClass   string       `json:"majorityFareClass,omitempty"`
	DistinctFareClasses []string     `json:"distinctFareClasses,omitempty"`
	ClassFlips          int          `json:"classFlips"`
	ExactMatchAccuracy  float64      `json:"exactMatchAccuracy"`
	Stability           float64      `json:"stability"`
	Stable              bool         `json:"stable"`
	MeanLatencyMs       float64      `json:"meanLatencyMs"`
	P95LatencyMs        float64      `json:"p95LatencyMs"`
	ExactMatches        int          `json:"exactMatches"`
	FailedCalls         int          `json:"failedCalls"`
}

// Summary aggregates prediction-level accuracy, per-item stability, and
// latency across the complete synthetic suite.
type Summary struct {
	TotalItems           int     `json:"totalItems"`
	TotalCalls           int     `json:"totalCalls"`
	SuccessfulCalls      int     `json:"successfulCalls"`
	FailedCalls          int     `json:"failedCalls"`
	ExactMatches         int     `json:"exactMatches"`
	ExactMatchAccuracy   float64 `json:"exactMatchAccuracy"`
	StableItems          int     `json:"stableItems"`
	StabilityRate        float64 `json:"stabilityRate"`
	MinimumItemStability float64 `json:"minimumItemStability"`
	MeanLatencyMs        float64 `json:"meanLatencyMs"`
	P95LatencyMs         float64 `json:"p95LatencyMs"`
	MaximumLatencyMs     float64 `json:"maximumLatencyMs"`
}

// Report is the machine-readable quality-gate artifact.
type Report struct {
	SchemaVersion string       `json:"schemaVersion"`
	Suite         string       `json:"suite"`
	GeneratedAt   time.Time    `json:"generatedAt"`
	Repeats       int          `json:"repeats"`
	Thresholds    Thresholds   `json:"thresholds"`
	Summary       Summary      `json:"summary"`
	Cases         []CaseReport `json:"cases"`
	Passed        bool         `json:"passed"`
	Failures      []string     `json:"failures,omitempty"`
}

// Runner makes repeated direct calls using the same request shape as the
// intake controller. Expected outcomes are read only by the comparison code;
// they are not part of the DecisionRequest.
type Runner struct {
	Client  decision.DecisionClient
	Policy  policy.Config
	Repeats int
	Now     func() time.Time
}

// Evaluate executes one or more sequential calls for every input case. Calls
// are intentionally sequential so latency and per-item stability are not
// confounded by evaluator-side concurrency.
func (r Runner) Evaluate(ctx context.Context, suiteName string, thresholds Thresholds, cases []InputCase) (Report, error) {
	if r.Client == nil {
		return Report{}, fmt.Errorf("evaluation decision client is required")
	}
	if r.Repeats < 1 {
		return Report{}, fmt.Errorf("evaluation repeats must be at least 1")
	}
	if len(cases) == 0 {
		return Report{}, fmt.Errorf("evaluation cases must not be empty")
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	for index, input := range cases {
		if strings.TrimSpace(input.WorkItemID) == "" || strings.TrimSpace(input.EventID) == "" {
			return Report{}, fmt.Errorf("case %d requires workItemID and eventID", index)
		}
		if !r.Policy.IsFareClass(input.Expected) {
			return Report{}, fmt.Errorf("case %s/%s has unsupported expected fare class %q", input.WorkItemID, input.EventID, input.Expected)
		}
	}

	report := Report{
		SchemaVersion: "v1",
		Suite:         suiteName,
		GeneratedAt:   r.Now().UTC(),
		Repeats:       r.Repeats,
		Thresholds:    thresholds,
		Cases:         make([]CaseReport, 0, len(cases)),
	}
	allLatencies := make([]float64, 0, len(cases)*r.Repeats)
	for _, input := range cases {
		caseReport := CaseReport{
			WorkItemID:        input.WorkItemID,
			EventID:           input.EventID,
			OccurredAt:        input.OccurredAt,
			ExpectedFareClass: canonicalFareClass(input.Expected),
			Rationale:         input.Rationale,
			Predictions:       make([]Prediction, 0, r.Repeats),
		}
		for attempt := 1; attempt <= r.Repeats; attempt++ {
			started := r.Now()
			result, err := r.Client.Decide(ctx, decision.DecisionRequest{
				Facts:        input.Facts,
				Instructions: r.Policy.Instructions,
				Choices:      r.Policy.DecisionOptions(),
			})
			latencyMs := float64(r.Now().Sub(started)) / float64(time.Millisecond)
			if latencyMs < 0 {
				latencyMs = 0
			}
			prediction := Prediction{Attempt: attempt, LatencyMs: latencyMs}
			allLatencies = append(allLatencies, latencyMs)
			report.Summary.TotalCalls++
			if err != nil {
				prediction.Error = err.Error()
				caseReport.FailedCalls++
				report.Summary.FailedCalls++
			} else {
				prediction.FareClass = canonicalFareClass(result.FareClass)
				prediction.Confidence = result.Confidence
				prediction.Provider = result.Provider
				caseReport.ExactMatches += boolInt(prediction.FareClass == caseReport.ExpectedFareClass)
				report.Summary.SuccessfulCalls++
				report.Summary.ExactMatches += boolInt(prediction.FareClass == caseReport.ExpectedFareClass)
			}
			caseReport.Predictions = append(caseReport.Predictions, prediction)
		}
		caseReport.ExactMatchAccuracy = ratio(caseReport.ExactMatches, r.Repeats)
		caseReport.Stability = predictionStability(caseReport.Predictions, r.Repeats)
		caseReport.MajorityFareClass, caseReport.DistinctFareClasses, caseReport.ClassFlips = predictionSummary(caseReport.Predictions)
		caseReport.Stable = caseReport.Stability >= 1 && caseReport.FailedCalls == 0
		caseReport.MeanLatencyMs = meanPredictionLatency(caseReport.Predictions)
		caseReport.P95LatencyMs = percentilePredictionLatency(caseReport.Predictions, 0.95)
		report.Summary.StableItems += boolInt(caseReport.Stable)
		report.Cases = append(report.Cases, caseReport)
	}

	report.Summary.TotalItems = len(report.Cases)
	report.Summary.ExactMatchAccuracy = ratio(report.Summary.ExactMatches, report.Summary.TotalCalls)
	report.Summary.StabilityRate = ratio(report.Summary.StableItems, report.Summary.TotalItems)
	report.Summary.MinimumItemStability = 1
	if len(report.Cases) > 0 {
		for _, item := range report.Cases {
			if item.Stability < report.Summary.MinimumItemStability {
				report.Summary.MinimumItemStability = item.Stability
			}
		}
	}
	report.Summary.MeanLatencyMs = mean(allLatencies)
	report.Summary.P95LatencyMs = percentile(allLatencies, 0.95)
	report.Summary.MaximumLatencyMs = max(allLatencies)
	report.Failures = thresholdFailures(report)
	report.Passed = len(report.Failures) == 0
	return report, nil
}

func thresholdFailures(report Report) []string {
	failures := make([]string, 0)
	if report.Summary.FailedCalls > 0 {
		failures = append(failures, fmt.Sprintf("%d decision calls failed", report.Summary.FailedCalls))
	}
	if report.Summary.ExactMatchAccuracy < report.Thresholds.MinimumExactMatchAccuracy {
		failures = append(failures, fmt.Sprintf("exact-match accuracy %.4f is below minimum %.4f", report.Summary.ExactMatchAccuracy, report.Thresholds.MinimumExactMatchAccuracy))
	}
	if report.Summary.MinimumItemStability < report.Thresholds.MinimumItemStability {
		failures = append(failures, fmt.Sprintf("minimum item stability %.4f is below minimum %.4f", report.Summary.MinimumItemStability, report.Thresholds.MinimumItemStability))
	}
	if report.Summary.P95LatencyMs > report.Thresholds.MaximumP95LatencyMs {
		failures = append(failures, fmt.Sprintf("p95 latency %.2fms exceeds maximum %.2fms", report.Summary.P95LatencyMs, report.Thresholds.MaximumP95LatencyMs))
	}
	return failures
}

func canonicalFareClass(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "first class", "first-class", "first", "fare-first-class":
		return policy.FareFirstClass
	case "business", "fare-business":
		return policy.FareBusiness
	case "economy", "fare-economy":
		return policy.FareEconomy
	default:
		return strings.TrimSpace(value)
	}
}

func predictionStability(predictions []Prediction, repeats int) float64 {
	if repeats <= 0 || len(predictions) == 0 {
		return 0
	}
	counts := make(map[string]int)
	for _, prediction := range predictions {
		if prediction.Error != "" || prediction.FareClass == "" {
			continue
		}
		counts[prediction.FareClass]++
	}
	maximum := 0
	for _, count := range counts {
		if count > maximum {
			maximum = count
		}
	}
	return ratio(maximum, repeats)
}

func predictionSummary(predictions []Prediction) (string, []string, int) {
	counts := make(map[string]int)
	ordered := make([]string, 0, len(predictions))
	previous := ""
	flips := 0
	for _, prediction := range predictions {
		if prediction.Error != "" || prediction.FareClass == "" {
			continue
		}
		counts[prediction.FareClass]++
		if previous != "" && previous != prediction.FareClass {
			flips++
		}
		previous = prediction.FareClass
		if !contains(ordered, prediction.FareClass) {
			ordered = append(ordered, prediction.FareClass)
		}
	}
	majority := ""
	maximum := 0
	for _, fareClass := range ordered {
		if counts[fareClass] > maximum {
			maximum = counts[fareClass]
			majority = fareClass
		}
	}
	return majority, ordered, flips
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func meanPredictionLatency(predictions []Prediction) float64 {
	values := make([]float64, 0, len(predictions))
	for _, prediction := range predictions {
		values = append(values, prediction.LatencyMs)
	}
	return mean(values)
}

func percentilePredictionLatency(predictions []Prediction, quantile float64) float64 {
	values := make([]float64, 0, len(predictions))
	for _, prediction := range predictions {
		values = append(values, prediction.LatencyMs)
	}
	return percentile(values, quantile)
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func percentile(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if quantile <= 0 {
		return min(values)
	}
	if quantile >= 1 {
		return max(values)
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	position := quantile * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower] + (sorted[upper]-sorted[lower])*weight
}

func min(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func max(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}

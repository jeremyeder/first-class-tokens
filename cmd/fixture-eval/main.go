// Command fixture-eval runs the synthetic classifier quality gate against a
// System One endpoint and writes a machine-readable report.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jeder/first-class-tokens/internal/decision"
	"github.com/jeder/first-class-tokens/internal/evaluation"
	"github.com/jeder/first-class-tokens/internal/policy"
)

func main() {
	workItemsPath := flag.String("work-items", "config/fixtures/business-work-items.yaml", "synthetic work-item metadata fixture")
	eventsPath := flag.String("events", "config/fixtures/business-events.jsonl", "synthetic business event JSONL fixture")
	expectedPath := flag.String("expected", "config/evaluation/expected-outcomes.yaml", "expected outcomes and quality thresholds")
	policyPath := flag.String("policy", "config/policy/fare-policy.yaml", "FarePolicy used to construct model choices")
	endpointDefault := os.Getenv("SYSTEMONE_URL")
	if endpointDefault == "" {
		endpointDefault = "http://127.0.0.1:8011"
	}
	endpoint := flag.String("endpoint", endpointDefault, "System One decision endpoint")
	output := flag.String("output", "-", "JSON report path, or - for stdout")
	repeats := flag.Int("repeats", 0, "override suite repeat count; zero uses the expected-outcomes file")
	timeout := flag.Duration("timeout", 30*time.Second, "per-decision request timeout")
	flag.Parse()

	suiteData := mustRead(*expectedPath)
	suite, err := evaluation.LoadSuite(suiteData)
	fatalIf(err)
	workItems := mustRead(*workItemsPath)
	events := mustRead(*eventsPath)
	cases, err := evaluation.LoadFixtureCases(workItems, events, suite)
	fatalIf(err)
	configuredPolicy, err := policy.LoadFile(*policyPath)
	fatalIf(err)
	client, err := decision.NewSystemOneClient(*endpoint, nil, *timeout)
	fatalIf(err)
	configuredRepeats := suite.Repeats
	if *repeats != 0 {
		if *repeats < 1 {
			fatalIf(fmt.Errorf("-repeats must be zero or at least one"))
		}
		configuredRepeats = *repeats
	}
	runner := evaluation.Runner{Client: client, Policy: configuredPolicy, Repeats: configuredRepeats}
	report, err := runner.Evaluate(context.Background(), suite.Name, suite.Thresholds, cases)
	fatalIf(err)
	encoded, err := json.MarshalIndent(report, "", "  ")
	fatalIf(err)
	encoded = append(encoded, '\n')
	if *output == "-" {
		if _, err := os.Stdout.Write(encoded); err != nil {
			fatalIf(err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
			fatalIf(fmt.Errorf("create report directory: %w", err))
		}
		if err := os.WriteFile(*output, encoded, 0o644); err != nil {
			fatalIf(fmt.Errorf("write report %q: %w", *output, err))
		}
		fmt.Fprintf(os.Stderr, "wrote quality-gate report to %s\n", *output)
	}
	fmt.Fprintf(os.Stderr, "quality gate: passed=%t items=%d calls=%d accuracy=%.4f min_stability=%.4f p95_latency_ms=%.2f\n", report.Passed, report.Summary.TotalItems, report.Summary.TotalCalls, report.Summary.ExactMatchAccuracy, report.Summary.MinimumItemStability, report.Summary.P95LatencyMs)
	for _, failure := range report.Failures {
		fmt.Fprintf(os.Stderr, "quality gate failure: %s\n", failure)
	}
	if !report.Passed {
		os.Exit(2)
	}
}

func mustRead(path string) []byte {
	data, err := os.ReadFile(path)
	fatalIf(err)
	return data
}

func fatalIf(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "fixture-eval: %v\n", err)
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	os.Exit(1)
}

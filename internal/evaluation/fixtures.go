// Package evaluation contains the repeatable quality gate for the synthetic
// business-priority inputs. The expected outcomes are deliberately loaded
// separately from the model request so they can only be used for comparison.
package evaluation

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	api "github.com/jeder/first-class-tokens/api/v1alpha1"
	"gopkg.in/yaml.v3"
)

// Thresholds are the quality-gate limits stored with an evaluation suite.
// Accuracy and stability are ratios in the inclusive range [0, 1].
type Thresholds struct {
	MinimumExactMatchAccuracy float64 `yaml:"minimumExactMatchAccuracy" json:"minimumExactMatchAccuracy"`
	MinimumItemStability      float64 `yaml:"minimumItemStability" json:"minimumItemStability"`
	MaximumP95LatencyMs       float64 `yaml:"maximumP95LatencyMs" json:"maximumP95LatencyMs"`
}

// ExpectedOutcome is evaluator-only data. It is never included in a
// DecisionRequest sent to System One.
type ExpectedOutcome struct {
	WorkItemID        string `yaml:"workItemID"`
	EventID           string `yaml:"eventID,omitempty"`
	ExpectedFareClass string `yaml:"expectedFareClass"`
	Rationale         string `yaml:"rationale,omitempty"`
}

// Suite is the normalized form of the checked-in expected-outcomes file.
type Suite struct {
	Name       string
	Repeats    int
	Thresholds Thresholds
	Outcomes   []ExpectedOutcome
}

type suiteDocument struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Repeats    int               `yaml:"repeats"`
		Thresholds Thresholds        `yaml:"thresholds"`
		Outcomes   []ExpectedOutcome `yaml:"outcomes"`
	} `yaml:"spec"`
}

// LoadSuite parses and validates a quality-gate configuration. Strict YAML
// decoding catches accidental schema drift before any model calls occur.
func LoadSuite(data []byte) (Suite, error) {
	var document suiteDocument
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return Suite{}, fmt.Errorf("decode evaluation suite: %w", err)
	}
	if document.APIVersion != "tokens.jeder.github.com/v1alpha1" || document.Kind != "FareQualityEvaluation" {
		return Suite{}, fmt.Errorf("evaluation suite has unexpected apiVersion/kind %q/%q", document.APIVersion, document.Kind)
	}
	if strings.TrimSpace(document.Metadata.Name) == "" {
		return Suite{}, fmt.Errorf("evaluation suite metadata.name is required")
	}
	if document.Spec.Repeats < 1 {
		return Suite{}, fmt.Errorf("evaluation suite spec.repeats must be at least 1")
	}
	if document.Spec.Thresholds.MinimumExactMatchAccuracy < 0 || document.Spec.Thresholds.MinimumExactMatchAccuracy > 1 {
		return Suite{}, fmt.Errorf("minimumExactMatchAccuracy must be in [0,1]")
	}
	if document.Spec.Thresholds.MinimumItemStability < 0 || document.Spec.Thresholds.MinimumItemStability > 1 {
		return Suite{}, fmt.Errorf("minimumItemStability must be in [0,1]")
	}
	if document.Spec.Thresholds.MaximumP95LatencyMs <= 0 {
		return Suite{}, fmt.Errorf("maximumP95LatencyMs must be greater than zero")
	}
	if len(document.Spec.Outcomes) == 0 {
		return Suite{}, fmt.Errorf("evaluation suite outcomes must not be empty")
	}
	seen := make(map[string]struct{}, len(document.Spec.Outcomes))
	for index, outcome := range document.Spec.Outcomes {
		if strings.TrimSpace(outcome.WorkItemID) == "" || strings.TrimSpace(outcome.ExpectedFareClass) == "" {
			return Suite{}, fmt.Errorf("outcome %d requires workItemID and expectedFareClass", index)
		}
		key := outcomeKey(outcome.WorkItemID, outcome.EventID)
		if _, exists := seen[key]; exists {
			return Suite{}, fmt.Errorf("duplicate expected outcome %q", key)
		}
		seen[key] = struct{}{}
	}
	return Suite{
		Name:       document.Metadata.Name,
		Repeats:    document.Spec.Repeats,
		Thresholds: document.Spec.Thresholds,
		Outcomes:   document.Spec.Outcomes,
	}, nil
}

// InputCase is one event snapshot evaluated repeatedly against the model.
// EventID makes multiple versions of the same work item independently
// evaluable, which is useful for demonstrating business-event churn.
type InputCase struct {
	WorkItemID string
	EventID    string
	OccurredAt time.Time
	Facts      api.BusinessFacts
	Expected   string
	Rationale  string
}

type fixtureWorkItem struct {
	ID         string `yaml:"id"`
	Source     string `yaml:"source"`
	SourceRef  string `yaml:"sourceRef"`
	Provenance string `yaml:"provenance"`
	Title      string `yaml:"title"`
}

type fixtureWorkItemFile struct {
	Items []fixtureWorkItem `yaml:"items"`
}

type fixtureEvent struct {
	EventID    string                     `json:"event_id"`
	OccurredAt string                     `json:"occurred_at"`
	WorkItemID string                     `json:"work_item_id"`
	Source     string                     `json:"source"`
	Provenance string                     `json:"provenance"`
	Facts      map[string]json.RawMessage `json:"facts"`
}

// LoadFixtureCases joins all checked-in business events to their metadata and
// expected outcomes. It intentionally does not inspect demo Job manifests:
// quality evaluation measures the classifier over business inputs, not queue
// projection or cluster capacity.
func LoadFixtureCases(workItemsYAML, eventsJSONL []byte, suite Suite) ([]InputCase, error) {
	var metadata fixtureWorkItemFile
	if err := yaml.Unmarshal(workItemsYAML, &metadata); err != nil {
		return nil, fmt.Errorf("decode work-item fixture: %w", err)
	}
	if len(metadata.Items) == 0 {
		return nil, fmt.Errorf("work-item fixture is empty")
	}
	itemsByID := make(map[string]fixtureWorkItem, len(metadata.Items))
	for _, item := range metadata.Items {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Source) == "" || strings.TrimSpace(item.SourceRef) == "" || strings.TrimSpace(item.Title) == "" {
			return nil, fmt.Errorf("work-item fixture %q is missing id, source, sourceRef, or title", item.ID)
		}
		if item.Provenance != string(api.ProvenanceSynthetic) {
			return nil, fmt.Errorf("work-item %s has non-synthetic provenance %q", item.ID, item.Provenance)
		}
		if _, exists := itemsByID[item.ID]; exists {
			return nil, fmt.Errorf("duplicate work-item id %q", item.ID)
		}
		itemsByID[item.ID] = item
	}

	outcomes := make(map[string]ExpectedOutcome, len(suite.Outcomes))
	for _, outcome := range suite.Outcomes {
		outcomes[outcomeKey(outcome.WorkItemID, outcome.EventID)] = outcome
	}

	events := make([]fixtureEvent, 0, len(metadata.Items))
	eventIDs := make(map[string]struct{})
	eventWorkItems := make(map[string]struct{}, len(metadata.Items))
	scanner := bufio.NewScanner(bytes.NewReader(eventsJSONL))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var event fixtureEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode business event line %d: %w", line, err)
		}
		if strings.TrimSpace(event.EventID) == "" || strings.TrimSpace(event.WorkItemID) == "" || strings.TrimSpace(event.Source) == "" || len(event.Facts) == 0 {
			return nil, fmt.Errorf("business event line %d is missing event_id, work_item_id, source, or facts", line)
		}
		if event.Provenance != string(api.ProvenanceSynthetic) {
			return nil, fmt.Errorf("event %s has non-synthetic provenance %q", event.EventID, event.Provenance)
		}
		if _, err := time.Parse(time.RFC3339, event.OccurredAt); err != nil {
			return nil, fmt.Errorf("event %s has invalid occurred_at %q", event.EventID, event.OccurredAt)
		}
		if _, exists := itemsByID[event.WorkItemID]; !exists {
			return nil, fmt.Errorf("event %s references unknown work item %q", event.EventID, event.WorkItemID)
		}
		if itemsByID[event.WorkItemID].Source != event.Source {
			return nil, fmt.Errorf("event %s source %q does not match work item %s source %q", event.EventID, event.Source, event.WorkItemID, itemsByID[event.WorkItemID].Source)
		}
		if _, exists := eventIDs[event.EventID]; exists {
			return nil, fmt.Errorf("duplicate event id %q", event.EventID)
		}
		eventIDs[event.EventID] = struct{}{}
		eventWorkItems[event.WorkItemID] = struct{}{}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan business events: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("business event fixture is empty")
	}
	for itemID := range itemsByID {
		if _, exists := eventWorkItems[itemID]; !exists {
			return nil, fmt.Errorf("work item %s has no business event", itemID)
		}
	}

	seenOutcomes := make(map[string]struct{}, len(events))
	cases := make([]InputCase, 0, len(events))
	for _, event := range events {
		item := itemsByID[event.WorkItemID]
		outcome, ok := outcomes[outcomeKey(event.WorkItemID, event.EventID)]
		if !ok {
			// A work-item-only outcome is convenient for a single-event item, but
			// is rejected when churn introduces multiple events.
			outcome, ok = outcomes[outcomeKey(event.WorkItemID, "")]
		}
		if !ok {
			return nil, fmt.Errorf("no expected outcome for work item %s event %s", event.WorkItemID, event.EventID)
		}
		key := outcomeKey(outcome.WorkItemID, outcome.EventID)
		if _, exists := seenOutcomes[key]; exists {
			return nil, fmt.Errorf("expected outcome %q matched more than one event; use eventID for churn", key)
		}
		seenOutcomes[key] = struct{}{}
		occurredAt, _ := time.Parse(time.RFC3339, event.OccurredAt)
		facts := make(map[string]string, len(event.Facts))
		for name, raw := range event.Facts {
			value, err := scalarFact(raw)
			if err != nil {
				return nil, fmt.Errorf("event %s fact %q: %w", event.EventID, name, err)
			}
			facts[name] = value
		}
		cases = append(cases, InputCase{
			WorkItemID: event.WorkItemID,
			EventID:    event.EventID,
			OccurredAt: occurredAt,
			Facts: api.BusinessFacts{
				StableID:       item.ID,
				Title:          item.Title,
				SourceCategory: api.SourceCategory(item.Source),
				SourceFacts:    facts,
			},
			Expected:  outcome.ExpectedFareClass,
			Rationale: outcome.Rationale,
		})
	}
	if len(seenOutcomes) != len(outcomes) {
		return nil, fmt.Errorf("expected outcomes contain %d entries but only %d matched fixture events", len(outcomes), len(seenOutcomes))
	}
	return cases, nil
}

func outcomeKey(workItemID, eventID string) string {
	return strings.TrimSpace(workItemID) + "\x00" + strings.TrimSpace(eventID)
}

func scalarFact(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", fmt.Errorf("value must be a non-null scalar")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	switch value.(type) {
	case string:
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", err
		}
		return text, nil
	case bool, float64:
		return string(raw), nil
	default:
		return "", fmt.Errorf("value must be a string, number, or boolean")
	}
}

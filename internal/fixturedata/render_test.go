package fixturedata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenderUsesEventFactsAndOnlyDeclaredJobs(t *testing.T) {
	root := filepath.Join("..", "..")
	workItems, err := os.ReadFile(filepath.Join(root, "config", "fixtures", "business-work-items.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := os.ReadFile(filepath.Join(root, "config", "fixtures", "business-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := os.ReadFile(filepath.Join(root, "manifests", "00-demo-lane.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	result, err := Render(workItems, events, jobs, "kueue-demo")
	if err != nil {
		t.Fatalf("render fixtures: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("generated %d BusinessWorkItems, want the 3 declared demo Jobs", len(result.Items))
	}
	if len(result.Skipped) != 17 {
		t.Fatalf("skipped %d fixture rows, want 17 without declared Jobs", len(result.Skipped))
	}

	renewal := result.Items[0]
	if renewal.Name != "work-item-001" || renewal.Spec.Link.JobName != "renewal-recovery-plan" {
		t.Fatalf("first generated resource has unexpected identity/link: %#v", renewal)
	}
	if renewal.Spec.Facts.SourceFacts["renewal_value_usd"] != "850000" || renewal.Spec.Facts.SourceFacts["customer_tier"] != "strategic" {
		t.Fatalf("source event facts were not copied: %#v", renewal.Spec.Facts.SourceFacts)
	}
	if renewal.Spec.Facts.BusinessState != "" || renewal.Spec.Facts.ValueTier != "" || renewal.Spec.Facts.Urgency != "" {
		t.Fatalf("renderer invented normalized business facts: %#v", renewal.Spec.Facts)
	}
	if renewal.Annotations["tokens.jeder.github.com/event-id"] != "synthetic-crm-001-v1" {
		t.Fatalf("event identity was not retained: %#v", renewal.Annotations)
	}
	if renewal.Spec.Provenance != "synthetic" || renewal.Namespace != "kueue-demo" {
		t.Fatalf("provenance or namespace was not retained: %#v", renewal)
	}
	for _, item := range result.Items {
		if item.Spec.Facts.SourceFacts == nil || len(item.Spec.Facts.SourceFacts) == 0 {
			t.Errorf("%s has no event facts", item.Name)
		}
		if _, includesClassHint := item.Annotations["class"]; includesClassHint {
			t.Errorf("%s unexpectedly contains a fixture class hint", item.Name)
		}
	}
}

func TestRenderAtVersionSelectsVersionedReplayForAllFixtureJobs(t *testing.T) {
	root := filepath.Join("..", "..")
	workItems, err := os.ReadFile(filepath.Join(root, "config", "fixtures", "business-work-items.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := os.ReadFile(filepath.Join(root, "config", "fixtures", "business-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	baseJobs, err := os.ReadFile(filepath.Join(root, "manifests", "00-demo-lane.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	fixtureJobs, err := os.ReadFile(filepath.Join(root, "manifests", "business-fixtures", "10-synthetic-jobs.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := append(append([]byte{}, baseJobs...), append([]byte("\n---\n"), fixtureJobs...)...)

	initial, err := RenderAtVersion(workItems, events, jobs, "kueue-demo", 1)
	if err != nil {
		t.Fatalf("render initial fixtures: %v", err)
	}
	if len(initial.Items) != 20 || len(initial.Skipped) != 0 {
		t.Fatalf("initial render = %d items, %d skipped; want 20 items and 0 skipped", len(initial.Items), len(initial.Skipped))
	}

	replay, err := RenderAtVersion(workItems, events, jobs, "kueue-demo", 2)
	if err != nil {
		t.Fatalf("render replay fixtures: %v", err)
	}
	if len(replay.Items) != 20 || len(replay.Skipped) != 0 {
		t.Fatalf("replay render = %d items, %d skipped; want 20 items and 0 skipped", len(replay.Items), len(replay.Skipped))
	}
	for _, item := range replay.Items {
		version := item.Annotations["tokens.jeder.github.com/event-version"]
		switch item.Name {
		case "work-item-001", "work-item-002", "work-item-003":
			if version != "2" {
				t.Errorf("%s event version = %q, want 2", item.Name, version)
			}
		default:
			if version != "1" {
				t.Errorf("%s event version = %q, want unchanged v1", item.Name, version)
			}
		}
	}

	updated := replay.Items[0]
	if updated.Annotations["tokens.jeder.github.com/event-id"] != "synthetic-crm-001-v2" {
		t.Fatalf("replay event id = %q, want synthetic-crm-001-v2", updated.Annotations["tokens.jeder.github.com/event-id"])
	}
	if updated.Spec.Facts.SourceFacts["renewal_stage"] != "renewed" || updated.Spec.Facts.SourceFacts["renewal_outcome"] != "confirmed" {
		t.Fatalf("replay facts were not selected: %#v", updated.Spec.Facts.SourceFacts)
	}
}

func TestRenderRejectsNestedEventFact(t *testing.T) {
	workItems := []byte("items:\n  - id: one\n    source: crm\n    sourceRef: one\n    linkedJob: renewal-recovery-plan\n    provenance: synthetic\n    title: One\n")
	events := []byte("{\"event_id\":\"event-one\",\"occurred_at\":\"2026-10-01T00:00:00Z\",\"work_item_id\":\"one\",\"source\":\"crm\",\"provenance\":\"synthetic\",\"facts\":{\"nested\":{\"a\":1}}}\n")
	jobs := []byte("kind: Job\nmetadata:\n  name: renewal-recovery-plan\n")
	if _, err := Render(workItems, events, jobs, "kueue-demo"); err == nil {
		t.Fatal("expected nested source fact to be rejected")
	}
}

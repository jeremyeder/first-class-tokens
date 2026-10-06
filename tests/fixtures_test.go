package fixtures_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workItemFixture struct {
	ID         string `yaml:"id"`
	Source     string `yaml:"source"`
	SourceRef  string `yaml:"sourceRef"`
	LinkedJob  string `yaml:"linkedJob"`
	Class      string `yaml:"class"`
	Priority   string `yaml:"currentPriorityClass"`
	Provenance string `yaml:"provenance"`
}

type workItemFixtureFile struct {
	Items []workItemFixture `yaml:"items"`
}

type businessEvent struct {
	EventID    string         `json:"event_id"`
	Version    int            `json:"version"`
	WorkItemID string         `json:"work_item_id"`
	Source     string         `json:"source"`
	Provenance string         `json:"provenance"`
	Facts      map[string]any `json:"facts"`
}

func TestSyntheticFixturesAreCompleteAndLinked(t *testing.T) {
	root := filepath.Join("..")
	workItemsPath := filepath.Join(root, "config", "fixtures", "business-work-items.yaml")
	eventsPath := filepath.Join(root, "config", "fixtures", "business-events.jsonl")

	data, err := os.ReadFile(workItemsPath)
	if err != nil {
		t.Fatalf("read work-item fixtures: %v", err)
	}
	var document workItemFixtureFile
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode work-item fixtures: %v", err)
	}

	if got := len(document.Items); got != 20 {
		t.Fatalf("work-item fixture count = %d, want 20", got)
	}

	validSources := map[string]bool{"crm": true, "jira": true, "strategy": true}
	itemsByID := make(map[string]workItemFixture, len(document.Items))
	for _, item := range document.Items {
		if item.ID == "" {
			t.Fatal("work-item fixture has an empty ID")
		}
		if _, exists := itemsByID[item.ID]; exists {
			t.Fatalf("duplicate work-item ID %q", item.ID)
		}
		itemsByID[item.ID] = item
		if item.Provenance != "synthetic" {
			t.Errorf("%s provenance = %q, want synthetic", item.ID, item.Provenance)
		}
		if !validSources[item.Source] {
			t.Errorf("%s source %q is not a supported synthetic source", item.ID, item.Source)
		}
		if item.SourceRef == "" || item.LinkedJob == "" {
			t.Errorf("%s must include sourceRef and linkedJob", item.ID)
		}
		if item.Class != "" || item.Priority != "" {
			t.Errorf("%s contains a static class hint; fixture classification must come from the model", item.ID)
		}
	}

	eventsFile, err := os.Open(eventsPath)
	if err != nil {
		t.Fatalf("open business events: %v", err)
	}
	defer eventsFile.Close()

	seenEvents := make(map[string]map[int]bool, len(document.Items))
	versionedUpdates := 0
	scanner := bufio.NewScanner(eventsFile)
	line := 0
	for scanner.Scan() {
		line++
		var event businessEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode business event line %d: %v", line, err)
		}
		if event.Version == 0 {
			event.Version = 1
		}
		if event.Version < 1 {
			t.Fatalf("business event line %d has invalid version %d", line, event.Version)
		}
		if event.EventID == "" || event.WorkItemID == "" || event.Source == "" || event.Facts == nil {
			t.Fatalf("business event line %d is missing required fields", line)
		}
		if event.Provenance != "synthetic" {
			t.Errorf("event %s provenance = %q, want synthetic", event.EventID, event.Provenance)
		}
		item, ok := itemsByID[event.WorkItemID]
		if !ok {
			t.Errorf("event %s references unknown work-item %q", event.EventID, event.WorkItemID)
			continue
		}
		if event.Source != item.Source {
			t.Errorf("event %s source = %q, want work-item source %q", event.EventID, event.Source, item.Source)
		}
		versions := seenEvents[event.WorkItemID]
		if versions == nil {
			versions = make(map[int]bool)
			seenEvents[event.WorkItemID] = versions
		}
		if versions[event.Version] {
			t.Errorf("work-item %s has duplicate fixture event version %d", event.WorkItemID, event.Version)
		}
		versions[event.Version] = true
		if event.Version > 1 {
			versionedUpdates++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan business events: %v", err)
	}
	if len(seenEvents) != len(itemsByID) {
		missing := make([]string, 0)
		for id := range itemsByID {
			if _, ok := seenEvents[id]; !ok {
				missing = append(missing, id)
			}
		}
		t.Fatalf("business events reference %d of %d work items; missing %s", len(seenEvents), len(itemsByID), strings.Join(missing, ", "))
	}
	if line != len(document.Items)+3 {
		t.Fatalf("business event count = %d, want %d v1 rows plus 3 controlled updates", line, len(document.Items))
	}
	if versionedUpdates != 3 {
		t.Fatalf("versioned update count = %d, want 3", versionedUpdates)
	}
}

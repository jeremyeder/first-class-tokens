package fixturedata

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	api "github.com/jeder/first-class-tokens/api/v1alpha1"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type WorkItem struct {
	ID         string `yaml:"id"`
	Source     string `yaml:"source"`
	SourceRef  string `yaml:"sourceRef"`
	LinkedJob  string `yaml:"linkedJob"`
	Provenance string `yaml:"provenance"`
	Title      string `yaml:"title"`
}

type workItemFile struct {
	Items []WorkItem `yaml:"items"`
}

type businessEvent struct {
	EventID    string                     `json:"event_id"`
	Version    int                        `json:"version"`
	OccurredAt string                     `json:"occurred_at"`
	WorkItemID string                     `json:"work_item_id"`
	Source     string                     `json:"source"`
	Provenance string                     `json:"provenance"`
	Facts      map[string]json.RawMessage `json:"facts"`
}

type jobManifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
}

// Result contains only BusinessWorkItems linked to Jobs declared in the
// supplied demo manifest. Skipped names are returned in fixture order.
type Result struct {
	Items   []api.BusinessWorkItem
	Skipped []string
}

// Render joins the initial source-event facts (version 1) to work-item
// metadata without importing the fixture's legacy class hints. Only items
// with a declared demo Job are emitted.
func Render(workItemsYAML, eventsJSONL, jobsYAML []byte, namespace string) (Result, error) {
	return RenderAtVersion(workItemsYAML, eventsJSONL, jobsYAML, namespace, 1)
}

// RenderAtVersion selects the newest event version less than or equal to
// maxVersion for each work item. A maxVersion of zero selects the newest event
// available. Keeping version selection in the renderer makes replay explicit
// and deterministic while preserving the initial v1 default for the demo.
func RenderAtVersion(workItemsYAML, eventsJSONL, jobsYAML []byte, namespace string, maxVersion int) (Result, error) {
	if strings.TrimSpace(namespace) == "" {
		return Result{}, fmt.Errorf("namespace is required")
	}
	if maxVersion < 0 {
		return Result{}, fmt.Errorf("event version must be zero (latest) or positive, got %d", maxVersion)
	}
	var metadata workItemFile
	if err := yaml.Unmarshal(workItemsYAML, &metadata); err != nil {
		return Result{}, fmt.Errorf("decode work-item fixture: %w", err)
	}
	if len(metadata.Items) == 0 {
		return Result{}, fmt.Errorf("work-item fixture is empty")
	}
	itemsByID := make(map[string]WorkItem, len(metadata.Items))
	for _, item := range metadata.Items {
		if item.ID == "" || item.Title == "" || item.LinkedJob == "" || item.SourceRef == "" {
			return Result{}, fmt.Errorf("work-item fixture %q is missing id, title, sourceRef, or linkedJob", item.ID)
		}
		if item.Provenance != string(api.ProvenanceSynthetic) {
			return Result{}, fmt.Errorf("work-item %s has non-synthetic provenance %q", item.ID, item.Provenance)
		}
		if _, exists := itemsByID[item.ID]; exists {
			return Result{}, fmt.Errorf("duplicate work-item id %q", item.ID)
		}
		itemsByID[item.ID] = item
	}

	jobs := make(map[string]bool)
	jobDecoder := yaml.NewDecoder(bytes.NewReader(jobsYAML))
	for {
		var job jobManifest
		err := jobDecoder.Decode(&job)
		if err == io.EOF {
			break
		}
		if err != nil {
			return Result{}, fmt.Errorf("decode demo Job manifest: %w", err)
		}
		if job.Kind == "Job" && job.Metadata.Name != "" {
			jobs[job.Metadata.Name] = true
		}
	}

	eventsByID := make(map[string]map[int]businessEvent, len(itemsByID))
	scanner := bufio.NewScanner(bytes.NewReader(eventsJSONL))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var event businessEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return Result{}, fmt.Errorf("decode business event line %d: %w", line, err)
		}
		if event.Version == 0 {
			// Existing v1 fixtures predate the explicit version field. Treating
			// an omitted value as v1 keeps those inputs backward compatible while
			// making all generated resources version-addressable.
			event.Version = 1
		}
		if event.Version < 1 {
			return Result{}, fmt.Errorf("business event line %d has invalid version %d", line, event.Version)
		}
		if event.EventID == "" || event.WorkItemID == "" || event.Source == "" || len(event.Facts) == 0 {
			return Result{}, fmt.Errorf("business event line %d is missing event_id, work_item_id, source, or facts", line)
		}
		if event.Provenance != string(api.ProvenanceSynthetic) {
			return Result{}, fmt.Errorf("event %s has non-synthetic provenance %q", event.EventID, event.Provenance)
		}
		if _, err := time.Parse(time.RFC3339, event.OccurredAt); err != nil {
			return Result{}, fmt.Errorf("event %s has invalid occurred_at %q", event.EventID, event.OccurredAt)
		}
		item, exists := itemsByID[event.WorkItemID]
		if !exists {
			return Result{}, fmt.Errorf("event %s references unknown work item %q", event.EventID, event.WorkItemID)
		}
		if item.Source != event.Source {
			return Result{}, fmt.Errorf("event %s source %q does not match work item %s source %q", event.EventID, event.Source, item.ID, item.Source)
		}
		versions := eventsByID[event.WorkItemID]
		if versions == nil {
			versions = make(map[int]businessEvent)
			eventsByID[event.WorkItemID] = versions
		}
		if _, exists := versions[event.Version]; exists {
			return Result{}, fmt.Errorf("work item %s has duplicate fixture event version %d", event.WorkItemID, event.Version)
		}
		versions[event.Version] = event
	}
	if err := scanner.Err(); err != nil {
		return Result{}, fmt.Errorf("scan business events: %w", err)
	}
	if len(eventsByID) != len(itemsByID) {
		return Result{}, fmt.Errorf("found events for %d work items, want %d", len(eventsByID), len(itemsByID))
	}

	result := Result{Items: make([]api.BusinessWorkItem, 0, len(jobs))}
	for _, item := range metadata.Items {
		event, ok := selectEvent(eventsByID[item.ID], maxVersion)
		if !ok {
			return Result{}, fmt.Errorf("work item %s has no event at or before version %d", item.ID, maxVersion)
		}
		if !jobs[item.LinkedJob] {
			result.Skipped = append(result.Skipped, item.ID)
			continue
		}
		facts := make(map[string]string, len(event.Facts))
		for key, raw := range event.Facts {
			value, err := scalarFact(raw)
			if err != nil {
				return Result{}, fmt.Errorf("event %s fact %q: %w", event.EventID, key, err)
			}
			facts[key] = value
		}
		result.Items = append(result.Items, api.BusinessWorkItem{
			TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "BusinessWorkItem"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      item.ID,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/part-of": "business-priority-demo",
					"business-source":           item.Source,
					"data-provenance":           string(api.ProvenanceSynthetic),
				},
				Annotations: map[string]string{
					"tokens.jeder.github.com/source-ref":    item.SourceRef,
					"tokens.jeder.github.com/event-id":      event.EventID,
					"tokens.jeder.github.com/occurred-at":   event.OccurredAt,
					"tokens.jeder.github.com/event-version": strconv.Itoa(event.Version),
				},
			},
			Spec: api.BusinessWorkItemSpec{
				Provenance: api.ProvenanceSynthetic,
				Facts: api.BusinessFacts{
					StableID:       item.ID,
					Title:          item.Title,
					SourceCategory: api.SourceCategory(item.Source),
					SourceFacts:    facts,
				},
				Link: api.JobLink{JobName: item.LinkedJob, Namespace: namespace},
			},
		})
	}
	return result, nil
}

func selectEvent(versions map[int]businessEvent, maxVersion int) (businessEvent, bool) {
	if len(versions) == 0 {
		return businessEvent{}, false
	}
	available := make([]int, 0, len(versions))
	for version := range versions {
		if maxVersion == 0 || version <= maxVersion {
			available = append(available, version)
		}
	}
	if len(available) == 0 {
		return businessEvent{}, false
	}
	sort.Ints(available)
	return versions[available[len(available)-1]], true
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

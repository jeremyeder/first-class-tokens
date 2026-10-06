package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jeder/first-class-tokens/internal/fixturedata"
)

type resourceList struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Items      []json.RawMessage `json:"items"`
}

func main() {
	workItemsPath := flag.String("work-items", "config/fixtures/business-work-items.yaml", "work-item metadata fixture")
	eventsPath := flag.String("events", "config/fixtures/business-events.jsonl", "business event JSONL fixture")
	jobsPath := flag.String("jobs", "manifests/00-demo-lane.yaml,manifests/business-fixtures/10-synthetic-jobs.yaml", "comma-separated demo Job manifests used to limit emitted items")
	namespace := flag.String("namespace", "kueue-demo", "target namespace for generated BusinessWorkItems")
	eventVersion := flag.Int("event-version", 1, "maximum event version to render; 0 selects the latest version")
	flag.Parse()

	workItems, err := os.ReadFile(*workItemsPath)
	fatalIf(err)
	events, err := os.ReadFile(*eventsPath)
	fatalIf(err)
	jobs, err := readJobManifests(*jobsPath)
	fatalIf(err)

	result, err := fixturedata.RenderAtVersion(workItems, events, jobs, *namespace, *eventVersion)
	fatalIf(err)
	if len(result.Items) == 0 {
		fatalIf(fmt.Errorf("no fixture work items are linked to Jobs in %s", *jobsPath))
	}

	resources := resourceList{APIVersion: "v1", Kind: "List", Items: make([]json.RawMessage, 0, len(result.Items))}
	for _, item := range result.Items {
		encoded, err := json.Marshal(item)
		fatalIf(err)
		var object map[string]json.RawMessage
		fatalIf(json.Unmarshal(encoded, &object))
		delete(object, "status")
		encoded, err = json.Marshal(object)
		fatalIf(err)
		resources.Items = append(resources.Items, encoded)
	}
	output, err := json.MarshalIndent(resources, "", "  ")
	fatalIf(err)
	if _, err := os.Stdout.Write(append(output, '\n')); err != nil {
		fatalIf(err)
	}
	fmt.Fprintf(os.Stderr, "rendered %d linked synthetic BusinessWorkItems at event version %d; skipped %d without declared demo Jobs\n", len(result.Items), *eventVersion, len(result.Skipped))
}

func readJobManifests(paths string) ([]byte, error) {
	var combined bytes.Buffer
	for _, path := range strings.Split(paths, ",") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read demo Job manifest %s: %w", path, err)
		}
		if combined.Len() > 0 {
			combined.WriteString("\n---\n")
		}
		combined.Write(data)
	}
	if combined.Len() == 0 {
		return nil, fmt.Errorf("no demo Job manifests supplied")
	}
	return combined.Bytes(), nil
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture-loader:", err)
		os.Exit(1)
	}
}

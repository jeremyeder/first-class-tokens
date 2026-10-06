package decision

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api "github.com/jeder/first-class-tokens/api/v1alpha1"
)

func TestHTTPSystemOneClientPostsV1SystemOne(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Fatalf("path = %q, want /v1/systemone", r.URL.Path)
		}
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request = %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request["model"] != "jev-latest" || request["steps"] != float64(1) {
			t.Fatalf("unexpected System One request: %#v", request)
		}
		state := request["state"].(map[string]any)
		facts := state["facts"].(map[string]any)
		sourceFacts := facts["sourceFacts"].(map[string]any)
		if sourceFacts["renewal_value_usd"] != "850000" {
			t.Fatalf("source facts were not sent to the model: %#v", sourceFacts)
		}
		questions := request["questions"].(map[string]any)
		fareClass := questions["fare_class"].(map[string]any)
		if fareClass["type"] != "choice" || fareClass["instructions"] != "use configured fare classes" {
			t.Fatalf("unexpected fare class question: %#v", fareClass)
		}
		criteria := fareClass["criteria"].(map[string]any)
		if criteria["first"] != "first definition" || criteria["business"] != "business definition" || criteria["economy"] != "economy definition" {
			t.Fatalf("unexpected policy choices: %#v", criteria)
		}
		_, _ = w.Write([]byte(`{"model":"dgemma","answers":{"fare_class":{"type":"choice","choice":"first","confidence":0.97,"probabilities":{"first":0.97,"business":0.02,"economy":0.01}}}}`))
	}))
	defer server.Close()

	client, err := NewHTTPSystemOneClient(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPSystemOneClient() error = %v", err)
	}
	got, err := client.Decide(context.Background(), DecisionRequest{
		Facts:        api.BusinessFacts{SourceFacts: map[string]string{"renewal_value_usd": "850000"}},
		Instructions: "use configured fare classes",
		Choices: map[string]string{
			"first": "first definition", "business": "business definition", "economy": "economy definition",
		},
	})
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if got.FareClass != "first" || got.Confidence != 0.97 || got.Provider != "systemone/dgemma" {
		t.Fatalf("unexpected decision: %+v", got)
	}
}

func TestHTTPSystemOneClientRejectsInvalidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"dgemma","answers":{"other":{"type":"choice","choice":"first","confidence":0.99}}}`))
	}))
	defer server.Close()
	client, err := NewHTTPSystemOneClient(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPSystemOneClient() error = %v", err)
	}
	if _, err := client.Decide(context.Background(), DecisionRequest{}); err == nil {
		t.Fatal("Decide() error = nil, want invalid response error")
	}
}

func TestHTTPSystemOneClientTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
	}))
	defer server.Close()
	client, err := NewHTTPSystemOneClient(server.URL, server.Client(), 10*time.Millisecond)
	if err != nil {
		t.Fatalf("NewHTTPSystemOneClient() error = %v", err)
	}
	_, err = client.Decide(context.Background(), DecisionRequest{})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Decide() error = %v, want ErrTimeout", err)
	}
}

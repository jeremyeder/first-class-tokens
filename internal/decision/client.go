// Package decision defines the typed boundary between Intake and System One.
package decision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	api "github.com/jeder/first-class-tokens/api/v1alpha1"
	"github.com/jeder/first-class-tokens/internal/policy"
)

// DecisionRequest is the complete local input sent to the direct model. It
// contains no credentials or Kubernetes object metadata.
type DecisionRequest struct {
	Facts           api.BusinessFacts `json:"facts"`
	CurrentFare     string            `json:"currentFareClass,omitempty"`
	CurrentPriority string            `json:"currentPriorityClass,omitempty"`
	Instructions    string            `json:"instructions,omitempty"`
	Choices         map[string]string `json:"choices,omitempty"`
}

// Request is a concise compatibility alias for callers building a provider
// fake or an HTTP request.
type Request = DecisionRequest

// Decision is the model response before policy validation. PriorityClass is
// optional; when present it must agree with the configured policy label.
type Decision struct {
	FareClass     string  `json:"fareClass"`
	PriorityClass string  `json:"priorityClass,omitempty"`
	Confidence    float64 `json:"confidence"`
	Reason        string  `json:"reason,omitempty"`
	SourceFact    string  `json:"sourceFact,omitempty"`
	Provider      string  `json:"provider,omitempty"`
}

// Response is a concise compatibility alias for the typed model result.
type Response = Decision

type systemOneRequest struct {
	Model     string         `json:"model"`
	State     any            `json:"state"`
	Questions map[string]any `json:"questions"`
	Steps     int            `json:"steps"`
}

type systemOneResponse struct {
	Model   string `json:"model"`
	Answers map[string]struct {
		Type          string             `json:"type"`
		Choice        string             `json:"choice"`
		Confidence    float64            `json:"confidence"`
		Probabilities map[string]float64 `json:"probabilities"`
	} `json:"answers"`
}

// DecisionClient is the typed, single decision boundary used by the
// controller. There is intentionally no shadow/challenger client.
type DecisionClient interface {
	Decide(ctx context.Context, request DecisionRequest) (Decision, error)
}

// ErrTimeout identifies a decision request that exceeded its context.
var ErrTimeout = errors.New("decision request timed out")

// ErrUnavailable is returned by the manager when no System One endpoint is
// configured. This keeps a misconfigured deployment fail-closed.
var ErrUnavailable = errors.New("decision provider unavailable")

// UnavailableClient is a fail-closed provider for deployments without a
// configured System One endpoint.
type UnavailableClient struct{}

func (UnavailableClient) Decide(ctx context.Context, request DecisionRequest) (Decision, error) {
	return Decision{}, ErrUnavailable
}

// HTTPSystemOneClient calls the System One decision endpoint directly.
type HTTPSystemOneClient struct {
	BaseURL   *url.URL
	Client    *http.Client
	Timeout   time.Duration
	UserAgent string
}

// NewHTTPSystemOneClient builds a client whose path is always /v1/systemone.
// An explicit http.Client is useful for focused tests; nil selects a bounded
// default client.
func NewHTTPSystemOneClient(baseURL string, client *http.Client, timeout time.Duration) (*HTTPSystemOneClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("invalid System One URL %q: %w", baseURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid System One URL %q", baseURL)
	}
	if client == nil {
		client = &http.Client{}
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &HTTPSystemOneClient{BaseURL: parsed, Client: client, Timeout: timeout, UserAgent: "first-class-tokens-intake/1"}, nil
}

// NewSystemOneClient is the short constructor name used by command wiring and
// tests that do not need to spell out the transport.
func NewSystemOneClient(baseURL string, client *http.Client, timeout time.Duration) (*HTTPSystemOneClient, error) {
	return NewHTTPSystemOneClient(baseURL, client, timeout)
}

// Decide posts one request to System One. Invalid status, JSON, or response
// shape is returned as an error so the controller can fail closed.
func (c *HTTPSystemOneClient) Decide(ctx context.Context, request DecisionRequest) (Decision, error) {
	if c == nil || c.BaseURL == nil || c.Client == nil {
		return Decision{}, ErrUnavailable
	}
	endpoint := *c.BaseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/systemone"
	choices := request.Choices
	if len(choices) == 0 {
		choices = map[string]string{
			"first":    "Material customer, security, or revenue risk requiring immediate attention.",
			"business": "Customer-impacting work with meaningful urgency.",
			"economy":  "Planned, deferrable, or low-urgency work.",
		}
	}
	instructions := strings.TrimSpace(request.Instructions)
	if instructions == "" {
		instructions = "Choose one fare class using the current business facts and the configured definitions. Do not infer facts that are not present."
	}
	payload, err := json.Marshal(systemOneRequest{
		Model: "jev-latest",
		State: map[string]any{
			"facts":                request.Facts,
			"currentFareClass":     request.CurrentFare,
			"currentPriorityClass": request.CurrentPriority,
		},
		Questions: map[string]any{
			"fare_class": map[string]any{
				"type":         "choice",
				"instructions": instructions,
				"criteria":     choices,
			},
		},
		Steps: 1,
	})
	if err != nil {
		return Decision{}, fmt.Errorf("marshal System One request: %w", err)
	}
	requestCtx := ctx
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), strings.NewReader(string(payload)))
	if err != nil {
		return Decision{}, fmt.Errorf("create System One request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return Decision{}, fmt.Errorf("%w: %v", ErrTimeout, err)
		}
		return Decision{}, fmt.Errorf("call System One: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Decision{}, fmt.Errorf("System One returned HTTP %d", resp.StatusCode)
	}
	var result systemOneResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Decision{}, fmt.Errorf("decode System One response: %w", err)
	}
	answer, ok := result.Answers["fare_class"]
	if !ok || answer.Type != "choice" || strings.TrimSpace(answer.Choice) == "" {
		return Decision{}, fmt.Errorf("invalid System One response: answers.fare_class.choice is required")
	}
	provider := "systemone"
	if result.Model != "" {
		provider += "/" + result.Model
	}
	return Decision{
		FareClass:  answer.Choice,
		Confidence: answer.Confidence,
		Reason:     instructions,
		SourceFact: factsSummary(request.Facts),
		Provider:   provider,
	}, nil
}

func factsSummary(facts api.BusinessFacts) string {
	return policy.FactsForAudit(facts)
}

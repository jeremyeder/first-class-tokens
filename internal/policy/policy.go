// Package policy contains the allow-listed queue projection policy.
package policy

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	api "github.com/jeder/first-class-tokens/api/v1alpha1"
	"sigs.k8s.io/yaml"
)

const (
	FareFirstClass = "First class"
	FareBusiness   = "Business"
	FareEconomy    = "Economy"
)

// Config maps model fare decisions to names already installed in Kueue. The
// controller never trusts a model-supplied arbitrary label; it only emits a
// label present in this configuration.
type Config struct {
	Labels         map[string]string
	PriorityValues map[string]int32
	Descriptions   map[string]string
	MinConfidence  float64
	Instructions   string
}

type policyFile struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Classes map[string]struct {
			KueuePriorityClass string `json:"kueuePriorityClass"`
			Definition         string `json:"definition"`
			PriorityValue      int32  `json:"priorityValue"`
		} `json:"classes"`
		Inputs     []string `json:"inputs"`
		HardGuards []string `json:"hardGuards"`
		Decision   struct {
			Model             string   `json:"model"`
			Choices           []string `json:"choices"`
			OnUncertain       string   `json:"onUncertain"`
			MinimumConfidence float64  `json:"minimumConfidence"`
			Instructions      string   `json:"instructions"`
		} `json:"decision"`
	} `json:"spec"`
}

// Projection is the validated, policy-owned result used by the controller.
type Projection struct {
	FareClass     string
	PriorityClass string
	PriorityValue int32
}

// Default returns the demo policy. Deployments may replace Labels and
// PriorityValues through manager flags/configuration without changing the
// controller or decision protocol.
func Default() Config {
	return Config{
		Labels: map[string]string{
			FareFirstClass: "fare-first-class",
			FareBusiness:   "fare-business",
			FareEconomy:    "fare-economy",
		},
		PriorityValues: map[string]int32{
			FareFirstClass: 1000,
			FareBusiness:   500,
			FareEconomy:    100,
		},
		Descriptions: map[string]string{
			FareFirstClass: "Material customer, security, or revenue risk requiring immediate attention.",
			FareBusiness:   "Customer-impacting work with meaningful urgency.",
			FareEconomy:    "Planned, deferrable, or low-urgency work.",
		},
		MinConfidence: 0.80,
		Instructions:  "Choose the fare class that best reflects the current business facts. Use only the supplied choices and definitions.",
	}
}

// LoadFile reads the deployable FarePolicy used to build classifier choices
// and the controller's allow-listed Kueue projection.
func LoadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read FarePolicy %q: %w", path, err)
	}
	var document policyFile
	if err := yaml.UnmarshalStrict(data, &document); err != nil {
		return Config{}, fmt.Errorf("parse FarePolicy %q: %w", path, err)
	}
	if document.Kind != "FarePolicy" || document.APIVersion != "tokens.jeder.github.com/v1alpha1" {
		return Config{}, fmt.Errorf("FarePolicy %q has unexpected apiVersion/kind %q/%q", path, document.APIVersion, document.Kind)
	}
	classNames := map[string]string{"first": FareFirstClass, "business": FareBusiness, "economy": FareEconomy}
	config := Config{
		Labels:         make(map[string]string, len(classNames)),
		PriorityValues: make(map[string]int32, len(classNames)),
		Descriptions:   make(map[string]string, len(classNames)),
		MinConfidence:  document.Spec.Decision.MinimumConfidence,
		Instructions:   document.Spec.Decision.Instructions,
	}
	for key, fareClass := range classNames {
		class, ok := document.Spec.Classes[key]
		if !ok || strings.TrimSpace(class.KueuePriorityClass) == "" || strings.TrimSpace(class.Definition) == "" || class.PriorityValue <= 0 {
			return Config{}, fmt.Errorf("FarePolicy class %q needs kueuePriorityClass, definition, and positive priorityValue", key)
		}
		config.Labels[fareClass] = class.KueuePriorityClass
		config.PriorityValues[fareClass] = class.PriorityValue
		config.Descriptions[fareClass] = class.Definition
	}
	if config.MinConfidence <= 0 || config.MinConfidence > 1 {
		return Config{}, fmt.Errorf("FarePolicy minimumConfidence must be greater than zero and at most one")
	}
	if strings.TrimSpace(config.Instructions) == "" {
		return Config{}, fmt.Errorf("FarePolicy decision.instructions is required")
	}
	return config, nil
}

// DecisionOptions returns the configured choice names and descriptions for
// the structured decision request. It does not let the model choose labels.
func (c Config) DecisionOptions() map[string]string {
	return map[string]string{
		"first":    c.Descriptions[FareFirstClass],
		"business": c.Descriptions[FareBusiness],
		"economy":  c.Descriptions[FareEconomy],
	}
}

// ErrInvalidDecision is returned when the direct model response cannot be
// mapped to the configured policy.
var ErrInvalidDecision = fmt.Errorf("invalid model decision")

// ErrLowConfidence is returned when a model response is below the configured
// minimum confidence. A low-confidence response is never projected.
var ErrLowConfidence = fmt.Errorf("model confidence below policy threshold")

// Project validates a direct model response and maps its fare class through
// the configured allow-list. Empty or malformed policy maps fail closed.
func (c Config) Project(fareClass, modelPriority string, confidence float64) (Projection, error) {
	if math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < c.MinConfidence || confidence > 1 {
		return Projection{}, ErrLowConfidence
	}
	class := normalizeFareClass(fareClass)
	label, ok := c.Labels[class]
	if !ok || strings.TrimSpace(label) == "" {
		return Projection{}, fmt.Errorf("%w: unsupported fare class %q", ErrInvalidDecision, fareClass)
	}
	if modelPriority != "" && modelPriority != label {
		return Projection{}, fmt.Errorf("%w: model priority %q does not match configured label %q", ErrInvalidDecision, modelPriority, label)
	}
	value, ok := c.PriorityValues[class]
	if !ok {
		return Projection{}, fmt.Errorf("%w: missing priority value for %q", ErrInvalidDecision, class)
	}
	return Projection{FareClass: class, PriorityClass: label, PriorityValue: value}, nil
}

// IsFareClass reports whether a value is an allow-listed fare class.
func (c Config) IsFareClass(fareClass string) bool {
	_, ok := c.Labels[normalizeFareClass(fareClass)]
	return ok
}

func normalizeFareClass(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "first class", "first-class", "first", "fare-first-class":
		return FareFirstClass
	case "business", "fare-business":
		return FareBusiness
	case "economy", "fare-economy":
		return FareEconomy
	default:
		return strings.TrimSpace(value)
	}
}

// FactsForAudit returns a stable, JSON-friendly source fact summary for audit
// entries. It intentionally keeps the source facts local to the CR.
func FactsForAudit(facts api.BusinessFacts) string {
	parts := []string{
		"stableID=" + facts.StableID,
		"state=" + string(facts.BusinessState),
		"valueTier=" + string(facts.ValueTier),
		"urgency=" + string(facts.Urgency),
	}
	keys := make([]string, 0, len(facts.SourceFacts))
	for key := range facts.SourceFacts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts = append(parts, "sourceFacts."+key+"="+facts.SourceFacts[key])
	}
	if facts.CostApproved != nil {
		parts = append(parts, fmt.Sprintf("costApproved=%t", *facts.CostApproved))
	}
	return strings.Join(parts, ";")
}

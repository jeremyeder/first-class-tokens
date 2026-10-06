package policy

import (
	"errors"
	"math"
	"path/filepath"
	"testing"

	api "github.com/jeder/first-class-tokens/api/v1alpha1"
)

func TestProjectUsesConfiguredLabelsAndValues(t *testing.T) {
	p := Default()
	p.Labels[FareFirstClass] = "priority-gold"
	p.PriorityValues[FareFirstClass] = 42
	got, err := p.Project("first-class", "priority-gold", 0.99)
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if got.FareClass != FareFirstClass || got.PriorityClass != "priority-gold" || got.PriorityValue != 42 {
		t.Fatalf("unexpected projection: %+v", got)
	}
}

func TestLoadFileUsesFarePolicyForClassifierAndProjection(t *testing.T) {
	path := filepath.Join("..", "..", "config", "policy", "fare-policy.yaml")
	config, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if config.MinConfidence != 0.80 || config.Labels[FareFirstClass] != "fare-first-class" || config.PriorityValues[FareEconomy] != 100 {
		t.Fatalf("unexpected policy config: %+v", config)
	}
	choices := config.DecisionOptions()
	if choices["first"] == "" || choices["business"] == "" || choices["economy"] == "" {
		t.Fatalf("FarePolicy did not populate classifier choices: %#v", choices)
	}
}

func TestProjectFailsClosed(t *testing.T) {
	p := Default()
	for name, confidence := range map[string]float64{
		"low":      0.79,
		"nan":      math.NaN(),
		"infinity": math.Inf(1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := p.Project(FareFirstClass, "", confidence); !errors.Is(err, ErrLowConfidence) {
				t.Fatalf("Project() error = %v, want ErrLowConfidence", err)
			}
		})
	}
	if _, err := p.Project("unknown", "", 0.99); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("invalid class error = %v, want ErrInvalidDecision", err)
	}
	if _, err := p.Project(FareFirstClass, "wrong-label", 0.99); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("mismatched label error = %v, want ErrInvalidDecision", err)
	}
}

func TestFactsForAuditIsStable(t *testing.T) {
	approved := true
	facts := api.BusinessFacts{StableID: "renewal-01", BusinessState: api.BusinessStateAtRisk, ValueTier: api.ValueTierHigh, Urgency: api.UrgencyHigh, CostApproved: &approved, SourceFacts: map[string]string{"days_to_renewal": "12", "renewal_value_usd": "850000"}}
	if got, want := FactsForAudit(facts), "stableID=renewal-01;state=at-risk;valueTier=high;urgency=high;sourceFacts.days_to_renewal=12;sourceFacts.renewal_value_usd=850000;costApproved=true"; got != want {
		t.Fatalf("FactsForAudit() = %q, want %q", got, want)
	}
}

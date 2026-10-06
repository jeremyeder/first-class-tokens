package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Provenance identifies where the business facts came from.  The first demo
// accepts only synthetic facts; keeping this as a typed value makes that
// boundary explicit in both the API and controller.
type Provenance string

const (
	ProvenanceSynthetic Provenance = "synthetic"
)

// SourceCategory identifies the business source represented by an item.
type SourceCategory string

const (
	SourceCRM          SourceCategory = "crm"
	SourceJira         SourceCategory = "jira"
	SourceStrategy     SourceCategory = "strategy"
	SourceIssueTracker SourceCategory = "issue-tracker"
	SourceSecurity     SourceCategory = "security"
	SourcePlannedWork  SourceCategory = "planned-work"
	SourceEvaluation   SourceCategory = "evaluation"
)

// BusinessState is deliberately a string enum so clients can display it and
// the CRD can validate it without coupling the API to a model implementation.
type BusinessState string

const (
	BusinessStateNew        BusinessState = "new"
	BusinessStateAtRisk     BusinessState = "at-risk"
	BusinessStateBlocked    BusinessState = "blocked"
	BusinessStateInProgress BusinessState = "in-progress"
	BusinessStateResolved   BusinessState = "resolved"
	BusinessStatePlanned    BusinessState = "planned"
	BusinessStateComplete   BusinessState = "complete"
)

// ValueTier describes the business value of the work item.
type ValueTier string

const (
	ValueTierStrategic ValueTier = "strategic"
	ValueTierHigh      ValueTier = "high"
	ValueTierStandard  ValueTier = "standard"
	ValueTierLow       ValueTier = "low"
)

// Urgency describes how quickly the business facts need action.
type Urgency string

const (
	UrgencyCritical Urgency = "critical"
	UrgencyHigh     Urgency = "high"
	UrgencyNormal   Urgency = "normal"
	UrgencyLow      Urgency = "low"
)

// BusinessFacts are the source facts sent to System One for a direct model
// decision.  StableID and Title live with the facts so an audit entry can be
// reconstructed without reaching back to an external business system.
type BusinessFacts struct {
	StableID       string            `json:"stableID"`
	Title          string            `json:"title"`
	SourceCategory SourceCategory    `json:"sourceCategory"`
	BusinessState  BusinessState     `json:"businessState,omitempty"`
	ValueTier      ValueTier         `json:"valueTier,omitempty"`
	Urgency        Urgency           `json:"urgency,omitempty"`
	CostApproved   *bool             `json:"costApproved,omitempty"`
	SourceFacts    map[string]string `json:"sourceFacts,omitempty"`
}

// JobLink identifies the one Job whose queue projection is owned by this
// item.  Namespace is optional; when empty, the BusinessWorkItem namespace is
// used.  UID is checked when supplied and is populated by the controller's
// discovery path when a linked Job is found.
type JobLink struct {
	Name string `json:"name,omitempty"`
	// JobName is accepted as an explicit alias for manifests that use the
	// Kubernetes term instead of the shorter link name.
	JobName   string    `json:"jobName,omitempty"`
	Namespace string    `json:"namespace,omitempty"`
	UID       types.UID `json:"uid,omitempty"`
}

// LinkedJobName returns the configured Job name regardless of which API alias
// was used by the manifest.
func (l JobLink) LinkedJobName() string {
	if l.JobName != "" {
		return l.JobName
	}
	return l.Name
}

// BusinessWorkItemSpec is the user-owned input to the intake controller.
// Status is the controller-owned output and must not be supplied by clients.
type BusinessWorkItemSpec struct {
	Facts      BusinessFacts `json:"facts"`
	Provenance Provenance    `json:"provenance"`
	Link       JobLink       `json:"link"`
}

// DecisionStatus records the last validated direct model response.
type DecisionStatus struct {
	FareClass     string       `json:"fareClass,omitempty"`
	PriorityClass string       `json:"priorityClass,omitempty"`
	PriorityValue int32        `json:"priorityValue,omitempty"`
	Confidence    float64      `json:"confidence,omitempty"`
	Reason        string       `json:"reason,omitempty"`
	SourceFact    string       `json:"sourceFact,omitempty"`
	Provider      string       `json:"provider,omitempty"`
	EvaluatedAt   *metav1.Time `json:"evaluatedAt,omitempty"`
}

// HistoryEntry is append-only audit material.  Outcome is deliberately
// explicit: a model decision can be valid while no Job write is permitted
// because the linked workload is admitted, reserved, or unavailable.
type HistoryEntry struct {
	ObservedAt            metav1.Time   `json:"observedAt"`
	PreviousBusinessState BusinessState `json:"previousBusinessState,omitempty"`
	BusinessState         BusinessState `json:"businessState"`
	PreviousFareClass     string        `json:"previousFareClass,omitempty"`
	FareClass             string        `json:"fareClass,omitempty"`
	PreviousPriorityClass string        `json:"previousPriorityClass,omitempty"`
	PriorityClass         string        `json:"priorityClass,omitempty"`
	Reason                string        `json:"reason,omitempty"`
	SourceFact            string        `json:"sourceFact,omitempty"`
	Outcome               string        `json:"outcome"`
	DecisionKey           string        `json:"decisionKey"`
}

// LinkedWorkloadStatus tells the UI why a valid decision did or did not
// change the linked Job.
type LinkedWorkloadStatus struct {
	Name       string `json:"name,omitempty"`
	State      string `json:"state,omitempty"`
	Admitted   bool   `json:"admitted,omitempty"`
	Reserved   bool   `json:"reserved,omitempty"`
	JobPatched bool   `json:"jobPatched,omitempty"`
}

// BusinessWorkItemStatus is controller-owned and auditable.
type BusinessWorkItemStatus struct {
	ObservedGeneration int64                `json:"observedGeneration,omitempty"`
	Decision           DecisionStatus       `json:"decision,omitempty"`
	FareClass          string               `json:"fareClass,omitempty"`
	PriorityClass      string               `json:"priorityClass,omitempty"`
	PriorityValue      int32                `json:"priorityValue,omitempty"`
	LinkedWorkload     LinkedWorkloadStatus `json:"linkedWorkload,omitempty"`
	History            []HistoryEntry       `json:"history,omitempty"`
	Conditions         []metav1.Condition   `json:"conditions,omitempty"`
}

// BusinessWorkItem is the namespaced business fact and its projected queue
// decision.
type BusinessWorkItem struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BusinessWorkItemSpec   `json:"spec,omitempty"`
	Status            BusinessWorkItemStatus `json:"status,omitempty"`
}

// BusinessWorkItemList contains BusinessWorkItems.
type BusinessWorkItemList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BusinessWorkItem `json:"items"`
}

const (
	ConditionDecision   = "DecisionReady"
	ConditionProjection = "ProjectionReady"
)

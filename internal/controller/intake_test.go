package controller

import (
	"context"
	"testing"
	"time"

	api "github.com/jeder/first-class-tokens/api/v1alpha1"
	"github.com/jeder/first-class-tokens/internal/decision"
	"github.com/jeder/first-class-tokens/internal/policy"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type fakeDecisionClient struct {
	decision decision.Decision
	err      error
	calls    int
}

func (f *fakeDecisionClient) Decide(_ context.Context, _ decision.DecisionRequest) (decision.Decision, error) {
	f.calls++
	return f.decision, f.err
}

func TestReconcilePatchesOnlyPendingUnreservedLinkedJob(t *testing.T) {
	item, job, workload := testObjects()
	provider := &fakeDecisionClient{decision: decision.Decision{FareClass: policy.FareFirstClass, Confidence: 0.99, Reason: "at risk"}}
	c := testClient(t, item, job, workload)
	r := NewIntakeReconciler(c, provider, policy.Default())
	r.ResyncPeriod = time.Hour
	r.Now = func() time.Time { return time.Unix(100, 0).UTC() }

	result, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(item)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != time.Hour {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, time.Hour)
	}
	if provider.calls != 1 {
		t.Fatalf("decision calls = %d, want 1", provider.calls)
	}
	gotJob := &batchv1.Job{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(job), gotJob); err != nil {
		t.Fatalf("get Job: %v", err)
	}
	if got := gotJob.Labels[PriorityLabel]; got != "fare-first-class" {
		t.Fatalf("Job priority label = %q, want fare-first-class", got)
	}
	gotItem := &api.BusinessWorkItem{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(item), gotItem); err != nil {
		t.Fatalf("get BusinessWorkItem: %v", err)
	}
	if gotItem.Status.PriorityClass != "fare-first-class" || len(gotItem.Status.History) != 1 {
		t.Fatalf("unexpected status: %+v", gotItem.Status)
	}
	if gotItem.Status.History[0].Outcome != "JobPatched" {
		t.Fatalf("history outcome = %q, want JobPatched", gotItem.Status.History[0].Outcome)
	}

	// The same facts are idempotent: the label and audit history remain stable.
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(item)}); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("unchanged facts triggered %d model decisions, want only the initial decision", provider.calls)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(item), gotItem); err != nil {
		t.Fatalf("get BusinessWorkItem after second reconcile: %v", err)
	}
	if len(gotItem.Status.History) != 1 {
		t.Fatalf("history entries after second reconcile = %d, want 1", len(gotItem.Status.History))
	}
}

func TestReconcileDoesNotPatchReservedWorkload(t *testing.T) {
	item, job, workload := testObjects()
	workload.Object["status"] = map[string]interface{}{
		"admission": map[string]interface{}{"clusterQueue": "demo"},
	}
	provider := &fakeDecisionClient{decision: decision.Decision{FareClass: policy.FareFirstClass, Confidence: 0.99}}
	c := testClient(t, item, job, workload)
	r := NewIntakeReconciler(c, provider, policy.Default())
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(item)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	gotJob := &batchv1.Job{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(job), gotJob); err != nil {
		t.Fatalf("get Job: %v", err)
	}
	if got := gotJob.Labels[PriorityLabel]; got != "fare-economy" {
		t.Fatalf("reserved Job priority label = %q, want unchanged fare-economy", got)
	}
	gotItem := &api.BusinessWorkItem{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(item), gotItem); err != nil {
		t.Fatalf("get BusinessWorkItem: %v", err)
	}
	if gotItem.Status.LinkedWorkload.State != workload.GetName() || !gotItem.Status.LinkedWorkload.Reserved {
		t.Fatalf("unexpected reserved status: %+v", gotItem.Status.LinkedWorkload)
	}
}

func TestReconcileFailsClosedForLowConfidenceAndProviderError(t *testing.T) {
	tests := []struct {
		name     string
		provider *fakeDecisionClient
		reason   string
	}{
		{name: "low confidence", provider: &fakeDecisionClient{decision: decision.Decision{FareClass: policy.FareFirstClass, Confidence: 0.2}}, reason: "LowConfidence"},
		{name: "timeout", provider: &fakeDecisionClient{err: decision.ErrTimeout}, reason: "DecisionTimeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item, job, workload := testObjects()
			c := testClient(t, item, job, workload)
			r := NewIntakeReconciler(c, tt.provider, policy.Default())
			if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(item)}); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			gotJob := &batchv1.Job{}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(job), gotJob); err != nil {
				t.Fatalf("get Job: %v", err)
			}
			if got := gotJob.Labels[PriorityLabel]; got != "fare-economy" {
				t.Fatalf("failed decision Job priority label = %q, want unchanged fare-economy", got)
			}
			gotItem := &api.BusinessWorkItem{}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(item), gotItem); err != nil {
				t.Fatalf("get BusinessWorkItem: %v", err)
			}
			condition := findCondition(gotItem.Status.Conditions, api.ConditionDecision)
			if condition == nil || condition.Reason != tt.reason || condition.Status != metav1.ConditionFalse {
				t.Fatalf("decision condition = %+v, want false/%s", condition, tt.reason)
			}
			if tt.reason == "LowConfidence" {
				if gotItem.Status.Decision.FareClass != policy.FareFirstClass || gotItem.Status.Decision.Confidence != 0.2 {
					t.Fatalf("rejected decision should remain auditable, got %+v", gotItem.Status.Decision)
				}
				if len(gotItem.Status.History) != 1 || gotItem.Status.History[0].Outcome != "NeedsReview" {
					t.Fatalf("low-confidence audit history = %+v", gotItem.Status.History)
				}
			}
		})
	}
}

func testObjects() (*api.BusinessWorkItem, *batchv1.Job, *unstructured.Unstructured) {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "renewal", Namespace: "kueue-demo", UID: types.UID("job-uid"), Labels: map[string]string{PriorityLabel: "fare-economy"}}}
	item := &api.BusinessWorkItem{ObjectMeta: metav1.ObjectMeta{Name: "renewal", Namespace: "kueue-demo", Generation: 1}, Spec: api.BusinessWorkItemSpec{
		Facts:      api.BusinessFacts{StableID: "renewal-01", Title: "Renewal", SourceCategory: api.SourceCRM, BusinessState: api.BusinessStateAtRisk, ValueTier: api.ValueTierHigh, Urgency: api.UrgencyHigh},
		Provenance: api.ProvenanceSynthetic,
		Link:       api.JobLink{Name: job.Name},
	}}
	workload := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kueue.x-k8s.io/v1beta2",
		"kind":       "Workload",
		"metadata": map[string]interface{}{
			"name":      "renewal-abc",
			"namespace": "kueue-demo",
			"ownerReferences": []interface{}{
				map[string]interface{}{"apiVersion": "batch/v1", "kind": "Job", "name": job.Name, "uid": string(job.UID)},
			},
		},
	}}
	return item, job, workload
}

func testClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatalf("add API scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batch scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.BusinessWorkItem{}).WithObjects(objects...).Build()
}

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

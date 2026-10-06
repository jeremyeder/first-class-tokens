// Package controller contains the Intake reconciliation boundary.
package controller

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	api "github.com/jeder/first-class-tokens/api/v1alpha1"
	"github.com/jeder/first-class-tokens/internal/decision"
	"github.com/jeder/first-class-tokens/internal/policy"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	PriorityLabel    = "kueue.x-k8s.io/priority-class"
	WorkloadGroup    = "kueue.x-k8s.io"
	WorkloadVersion  = "v1beta2"
	WorkloadKind     = "Workload"
	WorkloadListKind = "WorkloadList"
)

// DecisionClient is re-exported at the controller boundary so callers do not
// need to depend on the concrete HTTP client package.
type DecisionClient = decision.DecisionClient

// IntakeReconciler is the one Kubernetes write boundary for business queue
// projections. It updates only linked pending Jobs with no Workload
// reservation.
type IntakeReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	DecisionClient DecisionClient
	Policy         policy.Config
	ResyncPeriod   time.Duration
	Now            func() time.Time
}

// NewIntakeReconciler creates a reconciler with safe defaults.
func NewIntakeReconciler(c client.Client, decisionClient DecisionClient, p policy.Config) *IntakeReconciler {
	if len(p.Labels) == 0 || len(p.PriorityValues) == 0 {
		p = policy.Default()
	}
	return &IntakeReconciler{
		Client:         c,
		DecisionClient: decisionClient,
		Policy:         p,
		ResyncPeriod:   30 * time.Second,
		Now:            time.Now,
	}
}

// SetupWithManager registers the BusinessWorkItem, linked Job, and unstructured
// Kueue Workload watches. Periodic resync is returned from Reconcile so both
// object-triggered and timer-triggered paths use the same decision function.
func (r *IntakeReconciler) SetupWithManager(mgr manager.Manager) error {
	workload := &unstructured.Unstructured{}
	workload.SetGroupVersionKind(schema.GroupVersionKind{Group: WorkloadGroup, Version: WorkloadVersion, Kind: WorkloadKind})
	return builder.ControllerManagedBy(mgr).
		For(&api.BusinessWorkItem{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&batchv1.Job{}, handler.EnqueueRequestsFromMapFunc(r.mapLinkedResource)).
		Watches(workload, handler.EnqueueRequestsFromMapFunc(r.mapLinkedResource)).
		Complete(r)
}

// Reconcile evaluates the direct model once, validates it against policy, then
// discovers the Kueue Workload by linked Job UID before considering a label
// patch. All failure and no-op paths update auditable status and return nil so
// malformed/unsafe model output cannot accidentally write a Job.
func (r *IntakeReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	item := &api.BusinessWorkItem{}
	if err := r.Get(ctx, req.NamespacedName, item); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	if item.Spec.Provenance != api.ProvenanceSynthetic {
		return reconcile.Result{}, r.recordFailure(ctx, item, "NonSyntheticProvenance", "only synthetic facts are accepted")
	}
	jobName := item.Spec.Link.LinkedJobName()
	if strings.TrimSpace(jobName) == "" {
		return reconcile.Result{}, r.recordObservation(ctx, item, "NoLinkedJob", "no linked Job configured", "unlinked", false, false, "")
	}
	jobNamespace := item.Spec.Link.Namespace
	if jobNamespace == "" {
		jobNamespace = item.Namespace
	}
	if jobNamespace != item.Namespace {
		return reconcile.Result{}, r.recordObservation(ctx, item, "ForeignNamespace", "linked Job must be in the BusinessWorkItem namespace", "foreign-namespace", false, false, "")
	}
	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: jobNamespace, Name: jobName}, job); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, r.recordObservation(ctx, item, "LinkedJobNotFound", "linked Job was not found", "job-missing", false, false, "")
		}
		return reconcile.Result{}, err
	}
	if item.Spec.Link.UID != "" && item.Spec.Link.UID != job.UID {
		return reconcile.Result{}, r.recordObservation(ctx, item, "LinkedJobUIDMismatch", "linked Job UID does not match the configured link", "job-uid-mismatch", false, false, "")
	}
	if r.DecisionClient == nil {
		observeDecision("error", "DecisionProviderUnavailable", "", 0)
		return reconcile.Result{}, r.recordFailure(ctx, item, "DecisionProviderUnavailable", decision.ErrUnavailable.Error())
	}
	modelDecision := decision.Decision{}
	cachedDecision := item.Status.ObservedGeneration == item.Generation && item.Status.Decision.EvaluatedAt != nil
	if cachedDecision {
		intakeDecisionCacheHits.Inc()
		modelDecision = decision.Decision{
			FareClass:     item.Status.Decision.FareClass,
			PriorityClass: item.Status.Decision.PriorityClass,
			Confidence:    item.Status.Decision.Confidence,
			Reason:        item.Status.Decision.Reason,
			SourceFact:    item.Status.Decision.SourceFact,
			Provider:      item.Status.Decision.Provider,
		}
	} else {
		decisionStarted := time.Now()
		var decisionErr error
		modelDecision, decisionErr = r.DecisionClient.Decide(ctx, decision.DecisionRequest{
			Facts:           item.Spec.Facts,
			CurrentFare:     item.Status.FareClass,
			CurrentPriority: item.Status.PriorityClass,
			Instructions:    r.Policy.Instructions,
			Choices:         r.Policy.DecisionOptions(),
		})
		if decisionErr != nil {
			reason := "DecisionFailed"
			if errors.Is(decisionErr, decision.ErrTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = "DecisionTimeout"
			}
			observeDecisionDuration("error", time.Since(decisionStarted).Seconds())
			observeDecision("error", reason, "", 0)
			return reconcile.Result{RequeueAfter: r.resyncPeriod()}, r.recordFailure(ctx, item, reason, decisionErr.Error())
		}
		observeDecisionDuration("success", time.Since(decisionStarted).Seconds())
	}
	projection, err := r.Policy.Project(modelDecision.FareClass, modelDecision.PriorityClass, modelDecision.Confidence)
	if err != nil {
		reason := "InvalidDecision"
		if errors.Is(err, policy.ErrLowConfidence) {
			reason = "LowConfidence"
		}
		if !cachedDecision {
			observeDecision("rejected", reason, modelDecision.FareClass, modelDecision.Confidence)
		}
		return reconcile.Result{}, r.recordRejectedDecision(ctx, item, modelDecision, reason, err.Error())
	}
	if !cachedDecision {
		observeDecision("accepted", "DecisionAccepted", modelDecision.FareClass, modelDecision.Confidence)
	}
	workload, err := r.findWorkload(ctx, job.Namespace, job.UID)
	if err != nil {
		return reconcile.Result{}, err
	}
	if workload == nil {
		return reconcile.Result{}, r.recordDecisionObservation(ctx, item, modelDecision, projection, "WorkloadNotFound", "linked Kueue Workload was not found", "workload-missing", false, false, "")
	}
	state := workloadState(workload)
	if state.Reserved || state.Admitted {
		return reconcile.Result{}, r.recordDecisionObservation(ctx, item, modelDecision, projection, "WorkloadNotPending", "linked Kueue Workload is reserved or admitted", state.Name, false, state.Reserved, state.Name)
	}
	if state.Complete {
		return reconcile.Result{}, r.recordDecisionObservation(ctx, item, modelDecision, projection, "WorkloadComplete", "linked Kueue Workload is complete", state.Name, false, false, state.Name)
	}
	if err := r.patchJobPriority(ctx, job, projection.PriorityClass); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{RequeueAfter: r.resyncPeriod()}, r.recordDecisionObservation(ctx, item, modelDecision, projection, "JobPatched", "pending unreserved Job priority projected", "pending", true, false, state.Name)
}

func (r *IntakeReconciler) resyncPeriod() time.Duration {
	if r.ResyncPeriod <= 0 {
		return 30 * time.Second
	}
	return r.ResyncPeriod
}

func (r *IntakeReconciler) patchJobPriority(ctx context.Context, job *batchv1.Job, priorityClass string) error {
	if job.Labels != nil && job.Labels[PriorityLabel] == priorityClass {
		return nil
	}
	before := job.DeepCopy()
	after := job.DeepCopy()
	if after.Labels == nil {
		after.Labels = make(map[string]string)
	}
	after.Labels[PriorityLabel] = priorityClass
	return r.Patch(ctx, after, client.MergeFrom(before))
}

// findWorkload lists unstructured Kueue Workloads in the Job namespace and
// matches ownerReferences/known Job UID labels. It intentionally does not
// assume a generated Workload name.
func (r *IntakeReconciler) findWorkload(ctx context.Context, namespace string, jobUID types.UID) (*unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: WorkloadGroup, Version: WorkloadVersion, Kind: WorkloadListKind})
	if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	for i := range list.Items {
		candidate := &list.Items[i]
		if workloadMatchesJob(candidate, jobUID) {
			return candidate, nil
		}
	}
	return nil, nil
}

func workloadMatchesJob(workload *unstructured.Unstructured, jobUID types.UID) bool {
	for _, owner := range workload.GetOwnerReferences() {
		if owner.UID == jobUID && (owner.Kind == "Job" || owner.Kind == "BatchJob") {
			return true
		}
	}
	labels := workload.GetLabels()
	for _, key := range []string{"kueue.x-k8s.io/job-uid", "batch.kubernetes.io/job-uid", "job-uid"} {
		if labels[key] == string(jobUID) {
			return true
		}
	}
	return false
}

type observedWorkload struct {
	Name     string
	Reserved bool
	Admitted bool
	Complete bool
}

func workloadState(workload *unstructured.Unstructured) observedWorkload {
	state := observedWorkload{Name: workload.GetName()}
	if admission, ok, _ := unstructured.NestedMap(workload.Object, "status", "admission"); ok && admission != nil {
		state.Reserved = true
	}
	conditions, _, _ := unstructured.NestedSlice(workload.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		typeName, _, _ := unstructured.NestedString(condition, "type")
		status, _, _ := unstructured.NestedString(condition, "status")
		if status != string(metav1.ConditionTrue) {
			continue
		}
		switch typeName {
		case "Admitted", "QuotaReserved":
			state.Admitted = true
			state.Reserved = true
		case "Finished", "Complete", "Succeeded", "Failed":
			state.Complete = true
		}
	}
	return state
}

func (r *IntakeReconciler) mapLinkedResource(ctx context.Context, obj client.Object) []reconcile.Request {
	items := &api.BusinessWorkItemList{}
	if err := r.List(ctx, items, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	uid := obj.GetUID()
	jobName := obj.GetName()
	requests := make([]reconcile.Request, 0)
	for _, item := range items.Items {
		if item.Spec.Link.LinkedJobName() == jobName || (uid != "" && item.Spec.Link.UID == uid) {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: item.Namespace, Name: item.Name}})
		}
	}
	return requests
}

func (r *IntakeReconciler) recordFailure(ctx context.Context, item *api.BusinessWorkItem, reason, message string) error {
	status := item.DeepCopy()
	status.Status.ObservedGeneration = item.Generation
	setCondition(&status.Status, api.ConditionDecision, metav1.ConditionFalse, reason, message, r.now())
	return r.updateStatus(ctx, item, status)
}

func (r *IntakeReconciler) recordRejectedDecision(ctx context.Context, item *api.BusinessWorkItem, modelDecision decision.Decision, reason, message string) error {
	observeProjection("needs_review", reason, modelDecision.FareClass)
	status := item.DeepCopy()
	status.Status.ObservedGeneration = item.Generation
	now := r.now()
	status.Status.Decision = api.DecisionStatus{
		FareClass:   modelDecision.FareClass,
		Confidence:  modelDecision.Confidence,
		Reason:      modelDecision.Reason,
		SourceFact:  sourceFact(item, modelDecision),
		Provider:    modelDecision.Provider,
		EvaluatedAt: ptrTime(now),
	}
	appendHistory(&status.Status, item, api.HistoryEntry{
		ObservedAt:    metav1.NewTime(now),
		BusinessState: item.Spec.Facts.BusinessState,
		FareClass:     modelDecision.FareClass,
		Reason:        message,
		SourceFact:    sourceFact(item, modelDecision),
		Outcome:       "NeedsReview",
		DecisionKey:   statusKey(item.Generation, reason, modelDecision.FareClass, modelDecision.Confidence),
	})
	setCondition(&status.Status, api.ConditionDecision, metav1.ConditionFalse, reason, message, now)
	setCondition(&status.Status, api.ConditionProjection, metav1.ConditionFalse, reason, "Job priority unchanged", now)
	return r.updateStatus(ctx, item, status)
}

func (r *IntakeReconciler) recordObservation(ctx context.Context, item *api.BusinessWorkItem, reason, message, outcome string, patched, reserved bool, workloadName string) error {
	observeProjection(outcome, reason, "")
	status := item.DeepCopy()
	status.Status.ObservedGeneration = item.Generation
	status.Status.LinkedWorkload = api.LinkedWorkloadStatus{Name: workloadName, State: outcome, JobPatched: patched, Reserved: reserved}
	appendHistory(&status.Status, item, api.HistoryEntry{ObservedAt: metav1.NewTime(r.now()), BusinessState: item.Spec.Facts.BusinessState, Reason: message, Outcome: outcome, DecisionKey: statusKey(item.Generation, outcome, message)})
	setCondition(&status.Status, api.ConditionProjection, metav1.ConditionFalse, reason, message, r.now())
	return r.updateStatus(ctx, item, status)
}

func (r *IntakeReconciler) recordDecisionObservation(ctx context.Context, item *api.BusinessWorkItem, modelDecision decision.Decision, projection policy.Projection, reason, message, outcome string, patched, reserved bool, workloadName string) error {
	observeProjection(outcome, reason, projection.FareClass)
	status := item.DeepCopy()
	status.Status.ObservedGeneration = item.Generation
	now := r.now()
	applyDecision(&status.Status, item, modelDecision, projection, now)
	status.Status.LinkedWorkload = api.LinkedWorkloadStatus{Name: workloadName, State: outcome, JobPatched: patched, Reserved: reserved, Admitted: reserved}
	appendHistory(&status.Status, item, api.HistoryEntry{
		ObservedAt:    metav1.NewTime(now),
		BusinessState: item.Spec.Facts.BusinessState,
		FareClass:     projection.FareClass,
		PriorityClass: projection.PriorityClass,
		Reason:        message,
		SourceFact:    sourceFact(item, modelDecision),
		Outcome:       reason,
		DecisionKey:   statusKey(item.Generation, reason, projection.PriorityClass),
	})
	setCondition(&status.Status, api.ConditionDecision, metav1.ConditionTrue, "DecisionAccepted", "direct model decision passed policy validation", now)
	projectionStatus := metav1.ConditionFalse
	if patched {
		projectionStatus = metav1.ConditionTrue
	}
	setCondition(&status.Status, api.ConditionProjection, projectionStatus, reason, message, now)
	return r.updateStatus(ctx, item, status)
}

func applyDecision(status *api.BusinessWorkItemStatus, item *api.BusinessWorkItem, modelDecision decision.Decision, projection policy.Projection, now time.Time) {
	status.FareClass = projection.FareClass
	status.PriorityClass = projection.PriorityClass
	status.PriorityValue = projection.PriorityValue
	decisionStatus := api.DecisionStatus{
		FareClass:     projection.FareClass,
		PriorityClass: projection.PriorityClass,
		PriorityValue: projection.PriorityValue,
		Confidence:    modelDecision.Confidence,
		Reason:        modelDecision.Reason,
		SourceFact:    sourceFact(item, modelDecision),
		Provider:      modelDecision.Provider,
		EvaluatedAt:   ptrTime(now),
	}
	if status.Decision.FareClass == decisionStatus.FareClass &&
		status.Decision.PriorityClass == decisionStatus.PriorityClass &&
		status.Decision.Confidence == decisionStatus.Confidence &&
		status.Decision.Reason == decisionStatus.Reason &&
		status.Decision.SourceFact == decisionStatus.SourceFact &&
		status.Decision.Provider == decisionStatus.Provider {
		decisionStatus.EvaluatedAt = status.Decision.EvaluatedAt
	}
	status.Decision = decisionStatus
}

func sourceFact(item *api.BusinessWorkItem, modelDecision decision.Decision) string {
	if strings.TrimSpace(modelDecision.SourceFact) != "" {
		return modelDecision.SourceFact
	}
	return policy.FactsForAudit(item.Spec.Facts)
}

func appendHistory(status *api.BusinessWorkItemStatus, item *api.BusinessWorkItem, entry api.HistoryEntry) {
	entry.PreviousBusinessState = item.Spec.Facts.BusinessState
	entry.PreviousFareClass = item.Status.FareClass
	entry.PreviousPriorityClass = item.Status.PriorityClass
	if entry.BusinessState == "" {
		entry.BusinessState = item.Spec.Facts.BusinessState
	}
	if entry.DecisionKey == "" {
		entry.DecisionKey = statusKey(item.Generation, entry.Outcome, entry.FareClass, entry.PriorityClass)
	}
	if len(status.History) > 0 && status.History[len(status.History)-1].DecisionKey == entry.DecisionKey {
		return
	}
	status.History = append(status.History, entry)
}

func setCondition(status *api.BusinessWorkItemStatus, conditionType string, conditionStatus metav1.ConditionStatus, reason, message string, now time.Time) {
	for i := range status.Conditions {
		condition := &status.Conditions[i]
		if condition.Type != conditionType {
			continue
		}
		if condition.ObservedGeneration != status.ObservedGeneration {
			condition.ObservedGeneration = status.ObservedGeneration
		}
		if condition.Status == conditionStatus && condition.Reason == reason && condition.Message == message {
			return
		}
		condition.Status = conditionStatus
		condition.Reason = reason
		condition.Message = message
		condition.LastTransitionTime = metav1.NewTime(now)
		return
	}
	status.Conditions = append(status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             conditionStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: status.ObservedGeneration,
		LastTransitionTime: metav1.NewTime(now),
	})
}

func (r *IntakeReconciler) updateStatus(ctx context.Context, before, after *api.BusinessWorkItem) error {
	if equality.Semantic.DeepEqual(before.Status, after.Status) {
		return nil
	}
	return r.Status().Patch(ctx, after, client.MergeFrom(before))
}

func (r *IntakeReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func ptrTime(value time.Time) *metav1.Time {
	result := metav1.NewTime(value)
	return &result
}

func statusKey(parts ...interface{}) string {
	hash := fnv.New64a()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "%v\x00", part)
	}
	return fmt.Sprintf("%x", hash.Sum64())
}

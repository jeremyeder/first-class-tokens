package main

import (
	"flag"
	"os"
	"time"

	api "github.com/jeder/first-class-tokens/api/v1alpha1"
	intake "github.com/jeder/first-class-tokens/internal/controller"
	"github.com/jeder/first-class-tokens/internal/decision"
	"github.com/jeder/first-class-tokens/internal/policy"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	var systemOneURL string
	var timeout, resync time.Duration
	var firstClassLabel, businessLabel, economyLabel string
	var minConfidence float64
	var demoNamespace string
	var farePolicyFile string
	var metricsBindAddress string
	flag.StringVar(&systemOneURL, "systemone-url", os.Getenv("SYSTEMONE_URL"), "System One base URL; empty keeps the controller fail-closed")
	flag.StringVar(&demoNamespace, "demo-namespace", envOr("DEMO_NAMESPACE", "kueue-demo"), "namespace containing demo work items, Jobs, and Kueue Workloads")
	flag.StringVar(&farePolicyFile, "fare-policy-file", os.Getenv("FARE_POLICY_FILE"), "FarePolicy YAML path; an empty path uses built-in defaults")
	flag.StringVar(&metricsBindAddress, "metrics-bind-address", envOr("METRICS_BIND_ADDRESS", ":8080"), "Prometheus metrics bind address; set to 0 to disable")
	flag.DurationVar(&timeout, "decision-timeout", 60*time.Second, "maximum duration for a direct System One decision")
	flag.DurationVar(&resync, "resync-period", 30*time.Second, "periodic pending-workload resync interval")
	flag.StringVar(&firstClassLabel, "first-class-label", os.Getenv("FIRST_CLASS_PRIORITY_LABEL"), "optional override for the configured First class Kueue label")
	flag.StringVar(&businessLabel, "business-label", os.Getenv("BUSINESS_PRIORITY_LABEL"), "optional override for the configured Business Kueue label")
	flag.StringVar(&economyLabel, "economy-label", os.Getenv("ECONOMY_PRIORITY_LABEL"), "optional override for the configured Economy Kueue label")
	flag.Float64Var(&minConfidence, "min-confidence", 0, "optional override for FarePolicy minimum confidence")
	flag.Parse()

	ctrl.SetLogger(ctrlzap.New(ctrlzap.UseDevMode(false)))
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		panic(err)
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:  scheme,
		Cache:   cache.Options{DefaultNamespaces: map[string]cache.Config{demoNamespace: {}}},
		Metrics: metricsserver.Options{BindAddress: metricsBindAddress},
	})
	if err != nil {
		panic(err)
	}

	policyConfig := policy.Default()
	if farePolicyFile != "" {
		policyConfig, err = policy.LoadFile(farePolicyFile)
		if err != nil {
			panic(err)
		}
	}
	if firstClassLabel != "" {
		policyConfig.Labels[policy.FareFirstClass] = firstClassLabel
	}
	if businessLabel != "" {
		policyConfig.Labels[policy.FareBusiness] = businessLabel
	}
	if economyLabel != "" {
		policyConfig.Labels[policy.FareEconomy] = economyLabel
	}
	if minConfidence > 0 {
		policyConfig.MinConfidence = minConfidence
	}
	var provider decision.DecisionClient = decision.UnavailableClient{}
	if systemOneURL != "" {
		provider, err = decision.NewHTTPSystemOneClient(systemOneURL, nil, timeout)
		if err != nil {
			panic(err)
		}
	}
	reconciler := intake.NewIntakeReconciler(mgr.GetClient(), provider, policyConfig)
	reconciler.Scheme = scheme
	reconciler.ResyncPeriod = resync
	if err := reconciler.SetupWithManager(mgr); err != nil {
		panic(err)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		panic(err)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

var _ client.Object = (*api.BusinessWorkItem)(nil)

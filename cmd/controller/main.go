package main

import (
	"flag"
	"os"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	oubcontroller "github.com/tlrmchlsmth/oubliette/internal/controller"
	"github.com/tlrmchlsmth/oubliette/internal/evidence"
	"github.com/tlrmchlsmth/oubliette/internal/profile"
	"github.com/tlrmchlsmth/oubliette/internal/vcluster"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	var chartPath string
	var leaderElect bool
	var tombstoneRetention time.Duration
	var kueueClusterQueue string
	var kueueManagedLabel string
	var kueueManagedValue string
	var hostWorkloadQueue string
	var vclusterRunAsUser int64
	var vclusterRunAsGroup int64
	var vclusterFSGroup int64
	var vclusterEphemeralStorageRequest string
	var vclusterEphemeralStorageLimit string
	var metricsProfileGeneration string
	var metricsEndpointPrefix string
	var metricsIsolationScope string
	var metricsTrustDomain string
	var metricsServiceNamespace string
	var metricsServiceName string
	var evidenceStore string
	flag.StringVar(&chartPath, "vcluster-chart", "/charts/vcluster-0.36.1.tgz", "path to the pinned vCluster chart")
	flag.BoolVar(&leaderElect, "leader-elect", true, "enable leader election")
	flag.DurationVar(&tombstoneRetention, "tombstone-retention", time.Hour, "retention for completed TTL tombstones")
	flag.StringVar(&kueueClusterQueue, "kueue-cluster-queue", "", "host ClusterQueue for Oubliette LocalQueues; empty uses static capacity")
	flag.StringVar(&kueueManagedLabel, "kueue-managed-label", "kueue.x-k8s.io/managed-namespace", "namespace label key selected by host Kueue")
	flag.StringVar(&kueueManagedValue, "kueue-managed-value", "true", "namespace label value selected by host Kueue")
	flag.StringVar(&hostWorkloadQueue, "host-workload-queue", "", "fixed host LocalQueue label for nested vCluster system pods; empty keeps top-level control planes outside Kueue")
	flag.Int64Var(&vclusterRunAsUser, "vcluster-run-as-user", 1000, "numeric vCluster runtime user; set all runtime identity flags to zero for platform admission")
	flag.Int64Var(&vclusterRunAsGroup, "vcluster-run-as-group", 1000, "numeric vCluster runtime group; set all runtime identity flags to zero for platform admission")
	flag.Int64Var(&vclusterFSGroup, "vcluster-fs-group", 1000, "numeric vCluster filesystem group; set all runtime identity flags to zero for platform admission")
	flag.StringVar(&vclusterEphemeralStorageRequest, "vcluster-ephemeral-storage-request", "256Mi", "vCluster ephemeral-storage request; set request and limit empty to omit")
	flag.StringVar(&vclusterEphemeralStorageLimit, "vcluster-ephemeral-storage-limit", "4Gi", "vCluster ephemeral-storage limit; set request and limit empty to omit")
	flag.StringVar(&metricsProfileGeneration, "metrics-profile-generation", "", "trusted metrics profile generation; empty disables agent metrics access")
	flag.StringVar(&metricsEndpointPrefix, "metrics-endpoint-prefix", "metrics", "opaque metrics endpoint identity prefix")
	flag.StringVar(&metricsIsolationScope, "metrics-isolation-scope", "", "operator-declared metrics isolation scope")
	flag.StringVar(&metricsTrustDomain, "metrics-trust-domain", "", "operator-approved metrics trust domain")
	flag.StringVar(&metricsServiceNamespace, "metrics-service-namespace", "oubliette-system", "namespace containing the private metrics query gateway")
	flag.StringVar(&metricsServiceName, "metrics-service-name", "oubliette-metrics", "Service name of the private metrics query gateway")
	flag.StringVar(&evidenceStore, "evidence-store", "/var/lib/oubliette/evidence", "operator-owned directory for durable evidence bundles")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	vclusterValues := vcluster.ValuesOptions{
		HostWorkloadQueue: hostWorkloadQueue,
		RuntimeIdentity: &vcluster.RuntimeIdentity{
			User: vclusterRunAsUser, Group: vclusterRunAsGroup, FSGroup: vclusterFSGroup,
		},
		EphemeralStorage: &vcluster.ResourceBounds{
			Request: vclusterEphemeralStorageRequest, Limit: vclusterEphemeralStorageLimit,
		},
	}
	if err := vclusterValues.Validate(); err != nil {
		panic(err)
	}
	metricsProfile := profile.MetricsProfile{
		Enabled:        metricsProfileGeneration != "",
		Generation:     metricsProfileGeneration,
		EndpointPrefix: metricsEndpointPrefix,
		IsolationScope: metricsIsolationScope,
		TrustDomain:    metricsTrustDomain,
	}
	if metricsProfile.Enabled && (metricsProfile.IsolationScope == "" || metricsProfile.TrustDomain == "") {
		panic("metrics isolation scope and trust domain are required when metrics access is enabled")
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(oubv1.AddToScheme(scheme))
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: ":8081",
		LeaderElection:         leaderElect,
		LeaderElectionID:       "oubliette-controller",
	})
	if err != nil {
		panic(err)
	}
	helmManager := &vcluster.HelmManager{ChartPath: chartPath, Client: mgr.GetClient(), Values: vclusterValues}
	evidenceExporter := evidence.ConfigMapExporter{Client: mgr.GetClient(), Store: evidence.DirectoryStore{Root: evidenceStore}}
	if err := (&oubcontroller.OublietteReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		VCluster:                helmManager,
		TombstoneRetention:      tombstoneRetention,
		KueueClusterQueue:       kueueClusterQueue,
		KueueManagedLabel:       kueueManagedLabel,
		KueueManagedValue:       kueueManagedValue,
		MetricsProfile:          metricsProfile,
		MetricsServiceNamespace: metricsServiceNamespace,
		MetricsServiceName:      metricsServiceName,
		EvidenceExporter:        evidenceExporter,
	}).SetupWithManager(mgr); err != nil {
		panic(err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		panic(err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		panic(err)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		os.Exit(1)
	}
}

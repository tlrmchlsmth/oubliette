package main

import (
	"flag"
	"os"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	oubcontroller "github.com/tlrmchlsmth/oubliette/internal/controller"
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
	flag.StringVar(&chartPath, "vcluster-chart", "/charts/vcluster-0.36.1.tgz", "path to the pinned vCluster chart")
	flag.BoolVar(&leaderElect, "leader-elect", true, "enable leader election")
	flag.DurationVar(&tombstoneRetention, "tombstone-retention", time.Hour, "retention for completed TTL tombstones")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
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
	helmManager := &vcluster.HelmManager{ChartPath: chartPath, Client: mgr.GetClient()}
	if err := (&oubcontroller.OublietteReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), VCluster: helmManager, TombstoneRetention: tombstoneRetention}).SetupWithManager(mgr); err != nil {
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

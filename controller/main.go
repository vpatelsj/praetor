package main

import (
	"flag"
	"os"

	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
	"github.com/apollo/praetor/controller/reconcilers"
	"github.com/apollo/praetor/pkg/log"
	"github.com/apollo/praetor/pkg/version"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	scheme = clientgoscheme.Scheme
)

func init() {
	_ = apiv1alpha1.AddToScheme(scheme)
}

func main() {
	var metricsAddr string
	var probeAddr string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")

	log.Setup()
	flag.Parse()

	logger := ctrllog.Log.WithName("setup")
	logger.Info("starting controller manager", "version", version.Version, "commit", version.Commit)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), manager.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	reconciler := reconcilers.NewDeviceProcessDeploymentReconciler(
		mgr.GetClient(),
		mgr.GetScheme(),
		mgr.GetEventRecorderFor("deviceprocess-controller"),
	)

	if err := reconciler.SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create controller", "controller", "DeviceProcessDeployment")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}

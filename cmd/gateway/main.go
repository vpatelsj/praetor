package main

import (
	"context"
	"flag"
	"os"
	"time"

	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
	"github.com/apollo/praetor/gateway"
	"github.com/apollo/praetor/pkg/log"
	"github.com/apollo/praetor/pkg/version"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var scheme = clientgoscheme.Scheme

func init() {
	_ = apiv1alpha1.AddToScheme(scheme)
}

func main() {
	var addr string
	var probeAddr string
	var authToken string
	var authTokenSecret string
	var defaultHeartbeat int
	var staleMultiplier int

	flag.StringVar(&addr, "addr", ":8080", "address to serve HTTP gateway")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&authToken, "device-token", os.Getenv("APOLLO_GATEWAY_TOKEN"), "Shared device token expected in X-Device-Token header")
	flag.StringVar(&authTokenSecret, "device-token-secret", os.Getenv("APOLLO_GATEWAY_TOKEN_SECRET"), "Optional HMAC secret for per-device tokens")
	flag.IntVar(&defaultHeartbeat, "default-heartbeat-seconds", 15, "Default heartbeat interval if none provided by agent")
	flag.IntVar(&staleMultiplier, "stale-multiplier", 3, "Multiplier applied to heartbeat interval to decide staleness")

	log.Setup()
	flag.Parse()

	logger := ctrl.Log.WithName("gateway-setup")
	logger.Info("starting device gateway", "addr", addr, "version", version.Version, "commit", version.Commit)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), manager.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0", // disable metrics; HTTP server owns :8080
		},
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	ctx := context.Background()
	if err := mgr.GetFieldIndexer().IndexField(ctx, &apiv1alpha1.DeviceProcess{}, "spec.deviceRef.name", func(obj client.Object) []string {
		dp, ok := obj.(*apiv1alpha1.DeviceProcess)
		if !ok {
			return nil
		}
		if dp.Spec.DeviceRef.Name == "" {
			return nil
		}
		return []string{dp.Spec.DeviceRef.Name}
	}); err != nil {
		logger.Error(err, "unable to set deviceRef.name index")
		os.Exit(1)
	}

	gw := gateway.New(
		mgr.GetClient(),
		mgr.GetEventRecorderFor("device-gateway"),
		addr,
		authToken,
		authTokenSecret,
		time.Duration(defaultHeartbeat)*time.Second,
		staleMultiplier,
	)

	if err := mgr.Add(gw); err != nil {
		logger.Error(err, "unable to add gateway runnable")
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

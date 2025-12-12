package main

import (
	"flag"

	"github.com/apollo/praetor/pkg/log"
	"github.com/apollo/praetor/pkg/version"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

func main() {
	var deviceID string
	var kubeconfig string

	flag.StringVar(&deviceID, "device-id", "", "Identifier for the device")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig (optional)")

	log.Setup()
	flag.Parse()

	logger := ctrllog.Log.WithName("agent")

	if deviceID == "" {
		logger.Info("no device-id provided; running in anonymous mode")
	}

	logger.Info("agent starting", "deviceID", deviceID, "kubeconfig", kubeconfig, "version", version.Version, "commit", version.Commit)

	// Block until interrupted.
	select {}
}

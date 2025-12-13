package log

import (
	"flag"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// Setup initializes a zap logger configured for development by default.
func Setup() {
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	log.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
}

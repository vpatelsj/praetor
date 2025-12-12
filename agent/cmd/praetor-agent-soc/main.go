package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"agent/pkg/agent"
)

const deviceType = "soc"

func main() {
	var deviceID string
	var managerAddr string
	var labelFlags agent.LabelsFlag

	flag.StringVar(&deviceID, "device-id", getenv("DEVICE_ID"), "Unique device identifier")
	flag.StringVar(&managerAddr, "manager-address", getenvOrDefault("MANAGER_ADDRESS", "http://manager:8080"), "Praetor manager address")
	flag.Var(&labelFlags, "label", "Label in key=value form (repeatable)")
	flag.Parse()

	if deviceID == "" {
		log.Fatal("--device-id or DEVICE_ID is required")
	}

	labels, err := labelFlags.Map()
	if err != nil {
		log.Fatalf("invalid label: %v", err)
	}

	ag, err := agent.New(deviceID, deviceType, managerAddr, labels, log.Default())
	if err != nil {
		log.Fatalf("failed to init agent: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := ag.Start(ctx); err != nil && err != context.Canceled {
		log.Fatalf("agent exited with error: %v", err)
	}
}

func getenv(key string) string {
	return os.Getenv(key)
}

func getenvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

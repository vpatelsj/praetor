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

const deviceType = "simulator"

func main() {
	var deviceID string
	var managerAddr string

	flag.StringVar(&deviceID, "device-id", getenv("DEVICE_ID"), "Unique device identifier")
	flag.StringVar(&managerAddr, "manager-address", getenvOrDefault("MANAGER_ADDRESS", "http://manager:8080"), "Praetor manager address")
	flag.Parse()

	if deviceID == "" {
		log.Fatal("--device-id or DEVICE_ID is required")
	}

	ag, err := agent.New(deviceID, deviceType, managerAddr, log.Default())
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

package controllers

import (
	"sync"

	"manager/pkg/model"
	"manager/pkg/types"
)

// SimulatorController reconciles simulator rollouts.
type SimulatorController struct {
	mu         *sync.Mutex
	deviceType types.DeviceType
	rollouts   map[string]*model.Rollout
	devices    map[string]*model.Device
}

// NewSimulatorController constructs a simulator controller bound to shared rollout/device maps.
func NewSimulatorController(mu *sync.Mutex, rollouts map[string]*model.Rollout, devices map[string]*model.Device) *SimulatorController {
	return &SimulatorController{
		mu:         mu,
		deviceType: types.DeviceTypeSim,
		rollouts:   rollouts,
		devices:    devices,
	}
}

// ReconcileRollouts advances simulator rollouts.
func (c *SimulatorController) ReconcileRollouts() {
	c.mu.Lock()
	defer c.mu.Unlock()
	reconcileDeviceTypeRollouts(c.deviceType, c.rollouts, c.devices)
}

package controllers

import (
	"sync"

	"manager/pkg/model"
	"manager/pkg/types"
)

// ServerController reconciles server rollouts.
type ServerController struct {
	mu         *sync.Mutex
	deviceType types.DeviceType
	rollouts   map[string]*model.Rollout
	devices    map[string]*model.Device
}

// NewServerController constructs a server controller bound to shared rollout/device maps.
func NewServerController(mu *sync.Mutex, rollouts map[string]*model.Rollout, devices map[string]*model.Device) *ServerController {
	return &ServerController{
		mu:         mu,
		deviceType: types.DeviceTypeServer,
		rollouts:   rollouts,
		devices:    devices,
	}
}

// ReconcileRollouts advances server rollouts.
func (c *ServerController) ReconcileRollouts() {
	c.mu.Lock()
	defer c.mu.Unlock()
	reconcileDeviceTypeRollouts(c.deviceType, c.rollouts, c.devices)
}

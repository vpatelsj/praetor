package controllers

import (
	"sync"

	"manager/pkg/model"
	"manager/pkg/types"
)

type BMCController struct {
	mu         *sync.Mutex
	deviceType types.DeviceType
	rollouts   map[string]*model.Rollout
	devices    map[string]*model.Device
}

func NewBMCController(mu *sync.Mutex, rollouts map[string]*model.Rollout, devices map[string]*model.Device) *BMCController {
	return &BMCController{
		mu:         mu,
		deviceType: types.DeviceTypeBMC,
		rollouts:   rollouts,
		devices:    devices,
	}
}

func (c *BMCController) ReconcileRollouts() {
	c.mu.Lock()
	defer c.mu.Unlock()
	reconcileDeviceTypeRollouts(c.deviceType, c.rollouts, c.devices)
}

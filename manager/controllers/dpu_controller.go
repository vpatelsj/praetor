package controllers

import (
	"sync"

	"manager/pkg/model"
	"manager/pkg/types"
)

type DPUController struct {
	mu         *sync.Mutex
	deviceType types.DeviceType
	rollouts   map[string]*model.Rollout
	devices    map[string]*model.Device
}

func NewDPUController(mu *sync.Mutex, rollouts map[string]*model.Rollout, devices map[string]*model.Device) *DPUController {
	return &DPUController{
		mu:         mu,
		deviceType: types.DeviceTypeDPU,
		rollouts:   rollouts,
		devices:    devices,
	}
}

func (c *DPUController) ReconcileRollouts() {
	c.mu.Lock()
	defer c.mu.Unlock()
	reconcileDeviceTypeRollouts(c.deviceType, c.rollouts, c.devices)
}

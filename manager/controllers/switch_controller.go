package controllers

import (
	"sync"

	"manager/pkg/model"
	"manager/pkg/types"
)

type SwitchController struct {
	mu         *sync.Mutex
	deviceType types.DeviceType
	rollouts   map[string]*model.Rollout
	devices    map[string]*model.Device
}

func NewSwitchController(mu *sync.Mutex, rollouts map[string]*model.Rollout, devices map[string]*model.Device) *SwitchController {
	return &SwitchController{
		mu:         mu,
		deviceType: types.DeviceTypeSwitch,
		rollouts:   rollouts,
		devices:    devices,
	}
}

func (c *SwitchController) ReconcileRollouts() {
	c.mu.Lock()
	defer c.mu.Unlock()
	reconcileDeviceTypeRollouts(c.deviceType, c.rollouts, c.devices)
}

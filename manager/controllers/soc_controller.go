package controllers

import (
	"sync"

	"manager/pkg/model"
	"manager/pkg/types"
)

type SOCController struct {
	mu         *sync.Mutex
	deviceType types.DeviceType
	rollouts   map[string]*model.Rollout
	devices    map[string]*model.Device
}

func NewSOCController(mu *sync.Mutex, rollouts map[string]*model.Rollout, devices map[string]*model.Device) *SOCController {
	return &SOCController{
		mu:         mu,
		deviceType: types.DeviceTypeSOC,
		rollouts:   rollouts,
		devices:    devices,
	}
}

func (c *SOCController) ReconcileRollouts() {
	c.mu.Lock()
	defer c.mu.Unlock()
	reconcileDeviceTypeRollouts(c.deviceType, c.rollouts, c.devices)
}

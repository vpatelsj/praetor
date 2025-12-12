package controllers

import (
	"log"
	"strings"

	"manager/pkg/model"
	"manager/pkg/types"
)

func reconcileDeviceTypeRollouts(deviceType types.DeviceType, rollouts map[string]*model.Rollout, devices map[string]*model.Device) {
	if rollouts == nil {
		return
	}
	for name, rollout := range rollouts {
		reconcileRollout(deviceType, name, rollout, devices)
	}
}

func reconcileRollout(deviceType types.DeviceType, name string, rollout *model.Rollout, devices map[string]*model.Device) {
	if rollout == nil {
		return
	}
	if rollout.Spec.Selector == nil {
		rollout.Spec.Selector = map[string]string{}
	}
	if rollout.Status.UpdatedDevices == nil {
		rollout.Status.UpdatedDevices = map[string]bool{}
	}
	if rollout.Status.FailedDevices == nil {
		rollout.Status.FailedDevices = map[string]string{}
	}
	if rollout.Status.Generation == 0 {
		rollout.Status.Generation = 1
	}
	if rollout.Status.State == "" {
		rollout.Status.State = "Planned"
	}

	if rollout.Status.State == "Planned" {
		rollout.Status.State = "Running"
		rollout.Status.ObservedGeneration = rollout.Status.Generation
		rollout.Status.TotalTargets = countTargets(devices, rollout.Spec.Selector)
	}

	rollout.Status.SuccessCount = len(rollout.Status.UpdatedDevices)
	rollout.Status.FailureCount = len(rollout.Status.FailedDevices)

	total := rollout.Status.TotalTargets
	var failureRatio float64
	if total > 0 {
		failureRatio = float64(rollout.Status.FailureCount) / float64(total)
	}

	if total > 0 && failureRatio >= rollout.Spec.MaxFailures && rollout.Status.State == "Running" {
		rollout.Status.State = "Paused"
	}
	if total > 0 && rollout.Status.SuccessCount >= total {
		rollout.Status.State = "Succeeded"
	}

	log.Printf("[CONTROLLER][%s] rollout=%s state=%s success=%d failure=%d total=%d", deviceType, name, rollout.Status.State, rollout.Status.SuccessCount, rollout.Status.FailureCount, rollout.Status.TotalTargets)
}

func countTargets(devices map[string]*model.Device, selector map[string]string) int {
	if len(devices) == 0 {
		return 0
	}
	count := 0
	for _, dev := range devices {
		if dev == nil {
			continue
		}
		if matchesSelector(dev, selector) {
			count++
		}
	}
	return count
}

func matchesSelector(device *model.Device, selector map[string]string) bool {
	if len(selector) == 0 {
		return true
	}
	for k, v := range selector {
		key := strings.ToLower(k)
		switch key {
		case "deviceid", "device-id", "id":
			if !strings.EqualFold(device.ID, v) {
				return false
			}
		default:
			if device.Labels[key] != v {
				return false
			}
		}
	}
	return true
}

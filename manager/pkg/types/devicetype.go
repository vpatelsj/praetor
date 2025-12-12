package types

import (
	"fmt"
	"strings"
)

type DeviceType string

const (
	DeviceTypeSwitch DeviceType = "switch"
	DeviceTypeDPU    DeviceType = "dpu"
	DeviceTypeSOC    DeviceType = "soc"
	DeviceTypeBMC    DeviceType = "bmc"
	DeviceTypeServer DeviceType = "server"
	DeviceTypeSim    DeviceType = "simulator"
)

// ParseDeviceType normalizes a device type string into a supported DeviceType value.
func ParseDeviceType(s string) (DeviceType, error) {
	normalized := DeviceType(strings.ToLower(strings.TrimSpace(s)))
	switch normalized {
	case DeviceTypeSwitch, DeviceTypeDPU, DeviceTypeSOC, DeviceTypeBMC, DeviceTypeServer, DeviceTypeSim:
		return normalized, nil
	case "network-switch", "networkswitch", "networkswitches":
		return DeviceTypeSwitch, nil
	case "sim":
		return DeviceTypeSim, nil
	default:
		return "", fmt.Errorf("invalid device type: %s", s)
	}
}

// IsValid reports whether the DeviceType is among the supported constants.
func (dt DeviceType) IsValid() bool {
	switch dt {
	case DeviceTypeSwitch, DeviceTypeDPU, DeviceTypeSOC, DeviceTypeBMC, DeviceTypeServer, DeviceTypeSim:
		return true
	default:
		return false
	}
}

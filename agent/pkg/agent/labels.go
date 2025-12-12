package agent

import (
	"fmt"
	"strings"
)

// LabelsFlag collects repeated --label key=value flags and converts them to a map.
type LabelsFlag []string

func (l *LabelsFlag) String() string { return strings.Join(*l, ",") }

func (l *LabelsFlag) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("label must be key=value")
	}
	*l = append(*l, v)
	return nil
}

// Map returns the parsed label map.
func (l LabelsFlag) Map() (map[string]string, error) {
	return ParseLabels([]string(l))
}

// ParseLabels parses key=value pairs into a string map.
func ParseLabels(args []string) (map[string]string, error) {
	out := make(map[string]string)
	for _, kv := range args {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("label must be key=value")
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if key == "" || val == "" {
			return nil, fmt.Errorf("label must be key=value")
		}
		out[key] = val
	}
	return out, nil
}

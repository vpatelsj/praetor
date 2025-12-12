package cmd

import (
	"sort"
	"strings"
)

func valueOrDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func formatSelector(labels map[string]string) string {
	if len(labels) == 0 {
		return "<none>"
	}
	pairs := make([]string, 0, len(labels))
	for k, v := range labels {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

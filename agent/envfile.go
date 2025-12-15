package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
)

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func RenderEnvFile(vars []apiv1alpha1.DeviceProcessEnvVar) (string, error) {
	if len(vars) == 0 {
		return "", nil
	}

	items := make([]apiv1alpha1.DeviceProcessEnvVar, len(vars))
	copy(items, vars)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	b := &strings.Builder{}
	for _, v := range items {
		key := strings.TrimSpace(v.Name)
		if !envKeyPattern.MatchString(key) {
			return "", fmt.Errorf("invalid env var name %q", v.Name)
		}
		if strings.ContainsAny(v.Value, "\n\r") {
			return "", fmt.Errorf("invalid env var %q: value contains newline", key)
		}

		escaped := strings.ReplaceAll(v.Value, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		fmt.Fprintf(b, "%s=\"%s\"\n", key, escaped)
	}

	return b.String(), nil
}

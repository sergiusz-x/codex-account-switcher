package switcher

import "strings"

func setEnvironmentValue(environment []string, key, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if ok && environmentKeyEqual(name, key) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, key+"="+value)
}

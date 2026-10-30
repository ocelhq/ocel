package terraform

import "strings"

func addressParts(address string) []string {
	var parts []string
	depth, quoted, escaped, start := 0, false, false, 0
	for i, r := range address {
		switch {
		case escaped:
			escaped = false
		case quoted && r == '\\':
			escaped = true
		case r == '"':
			quoted = !quoted
		case quoted:
		case r == '[':
			depth++
		case r == ']':
			depth--
		case r == '.' && depth == 0:
			parts = append(parts, address[start:i])
			start = i + 1
		}
	}
	return append(parts, address[start:])
}

func parentModule(address string) string {
	parts := addressParts(address)
	if len(parts) <= 2 {
		return ""
	}
	return strings.Join(parts[:len(parts)-2], ".")
}

func moduleCalls(address string) []string {
	if address == "" {
		return nil
	}
	parts := addressParts(address)
	calls := make([]string, 0, len(parts)/2)
	for i := 1; i < len(parts); i += 2 {
		name, _, _ := strings.Cut(parts[i], "[")
		calls = append(calls, name)
	}
	return calls
}

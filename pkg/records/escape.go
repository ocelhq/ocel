package records

import "strings"

func Escape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "%", "%25"), "/", "%2F")
}

func Unescape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "%2F", "/"), "%25", "%")
}

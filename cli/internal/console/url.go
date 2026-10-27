package console

import (
	"cmp"
	"os"
	"strings"
)

const DefaultBaseURL = "https://app.ocelhq.com"

const (
	URLEnvVar       = "OCEL_CONSOLE_URL"
	ProjectIDEnvVar = "OCEL_CONSOLE_PROJECT_ID"
)

func BaseURL(stored string) string {
	return NormalizeBaseURL(cmp.Or(strings.TrimSpace(os.Getenv(URLEnvVar)), stored))
}

func NormalizeBaseURL(raw string) string {
	return strings.TrimRight(cmp.Or(strings.TrimSpace(raw), DefaultBaseURL), "/")
}

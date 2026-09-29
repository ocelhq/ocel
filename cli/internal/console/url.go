package console

import (
	"os"
	"strings"
)

const DefaultBaseURL = "https://ocel.app"

const URLEnvVar = "OCEL_CONSOLE_URL"

func BaseURL(stored string) string {
	chosen := strings.TrimSpace(os.Getenv(URLEnvVar))
	if chosen == "" {
		chosen = strings.TrimSpace(stored)
	}
	if chosen == "" {
		chosen = DefaultBaseURL
	}
	return strings.TrimRight(chosen, "/")
}

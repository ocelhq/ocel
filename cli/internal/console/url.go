package console

import (
	"os"
	"strings"
)

const DefaultBaseURL = "https://ocel.app"

const URLEnvVar = "OCEL_CONSOLE_URL"

const (
	localConsoleEnvVar = "OCEL_DEV"
	localBaseURL       = "http://localhost:3000"
)

func BaseURL(stored string) string {
	chosen := strings.TrimSpace(os.Getenv(URLEnvVar))
	if chosen == "" {
		chosen = strings.TrimSpace(stored)
	}
	if chosen == "" {
		chosen = DefaultBaseURL
		if os.Getenv(localConsoleEnvVar) != "" {
			chosen = localBaseURL
		}
	}
	return strings.TrimRight(chosen, "/")
}

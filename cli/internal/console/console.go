package console

import (
	"os"
	"strings"
)

const DefaultBaseURL = "https://ocel.app"

const URLEnvVar = "OCEL_CONSOLE_URL"

func ResolveBaseURL() string {
	if v := strings.TrimSpace(os.Getenv(URLEnvVar)); v != "" {
		return v
	}
	if os.Getenv("OCEL_DEV") != "" {
		return "http://localhost:3000"
	}
	return DefaultBaseURL
}

func EffectiveBaseURL(credsURL string) string {
	if v := strings.TrimSpace(os.Getenv(URLEnvVar)); v != "" {
		return strings.TrimRight(v, "/")
	}
	if credsURL != "" {
		return credsURL
	}
	return ResolveBaseURL()
}

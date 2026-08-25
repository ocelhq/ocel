package console

import (
	"os"
	"strings"
)

const DefaultBaseURL = "https://ocel.app"

func ResolveBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("OCEL_API_URL")); v != "" {
		return v
	}
	if os.Getenv("OCEL_DEV") != "" {
		return "http://localhost:3000"
	}
	return DefaultBaseURL
}

func EffectiveBaseURL(credsURL string) string {
	if v := strings.TrimSpace(os.Getenv("OCEL_API_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	if credsURL != "" {
		return credsURL
	}
	return ResolveBaseURL()
}

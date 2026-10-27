package console

import (
	"cmp"
	"fmt"
	"net/url"
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

var defaultPorts = map[string]string{"http": "80", "https": "443"}

func Origin(apiURL string) (string, error) {
	parsed, err := url.Parse(apiURL)
	if err != nil || defaultPorts[parsed.Scheme] == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("the console URL %q is not an http or https URL", apiURL)
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" && port != defaultPorts[parsed.Scheme] {
		host += ":" + port
	}
	return parsed.Scheme + "://" + host, nil
}

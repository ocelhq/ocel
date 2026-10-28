package ports

import (
	"net/http"
	"strings"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/content"
)

const storageService = "storage"

var servicesWithSecretContent = map[string]bool{
	"iamcredentials": true,
	"oauth2":         true,
	"run":            true,
	"secretmanager":  true,
	"sqladmin":       true,
	"sts":            true,
}

func NewGoogleTransport(base http.RoundTripper) http.RoundTripper {
	return secretContentTransport{base: &tracing.Transport{Base: base, Name: nameRESTMethod}}
}

type secretContentTransport struct {
	base http.RoundTripper
}

func (t secretContentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if carriesSecretContent(req) {
		req = req.WithContext(content.Omit(req.Context()))
	}
	return t.base.RoundTrip(req)
}

var credentialHosts = map[string]bool{
	"accounts.google.com":      true,
	"metadata.google.internal": true,
	"169.254.169.254":          true,
}

func carriesSecretContent(req *http.Request) bool {
	service, _ := readService(req)
	switch {
	case credentialHosts[strings.ToLower(req.URL.Hostname())], servicesWithSecretContent[service], strings.HasSuffix(req.URL.Path, "/token"):
		return true
	case service == storageService:
		return !strings.HasPrefix(req.URL.Path, "/storage/v1/") || isObjectMedia(req)
	default:
		return isObjectMedia(req)
	}
}

func isObjectMedia(req *http.Request) bool {
	return strings.HasPrefix(req.URL.Path, "/upload/") || strings.HasPrefix(req.URL.Path, "/download/") || req.URL.Query().Get("alt") == "media"
}

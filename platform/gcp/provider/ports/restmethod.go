package ports

import (
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/tracing"
	"go.opentelemetry.io/otel/attribute"
)

const (
	restSystem     = "gcp-api"
	googleAPIsHost = ".googleapis.com"
	mutualTLSLabel = ".mtls"
)

var (
	regionalHost   = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+-`)
	versionSegment = regexp.MustCompile(`^v[0-9]+(?:(?:alpha|beta)[0-9]*)?$`)
)

func nameRESTMethod(req *http.Request) (string, []attribute.KeyValue, bool) {
	service, method, ok := readRESTMethod(req)
	if !ok {
		return "", nil, false
	}
	name, attrs := tracing.NameRPCSpan(restSystem, service, method)
	return name, attrs, true
}

func readService(req *http.Request) (string, bool) {
	service, ok := strings.CutSuffix(strings.ToLower(req.URL.Hostname()), googleAPIsHost)
	if !ok {
		return "", false
	}
	service = regionalHost.ReplaceAllString(strings.TrimSuffix(service, mutualTLSLabel), "")
	if strings.HasSuffix(service, "."+storageService) {
		return storageService, true
	}
	return service, true
}

func readRESTMethod(req *http.Request) (service, method string, ok bool) {
	service, ok = readService(req)
	if !ok {
		return "", "", false
	}
	segments := strings.Split(strings.Trim(req.URL.EscapedPath(), "/"), "/")
	version := slices.IndexFunc(segments, versionSegment.MatchString)
	if version < 0 || version == len(segments)-1 {
		return "", "", false
	}
	resource := segments[version+1:]
	last, verb, custom := strings.Cut(resource[len(resource)-1], ":")
	resource[len(resource)-1] = last
	words := make([]string, 0, len(resource)/2+2)
	for i := 0; i < len(resource); i += 2 {
		words = append(words, resource[i])
	}
	if !custom {
		verb = restVerb(req.Method, len(resource)%2 == 1)
	}
	return service, strings.Join(append(words, verb), "."), true
}

func restVerb(method string, isCollection bool) string {
	switch method {
	case http.MethodGet:
		if isCollection {
			return "list"
		}
		return "get"
	case http.MethodPost:
		return "create"
	case http.MethodPut:
		return "update"
	default:
		return strings.ToLower(method)
	}
}

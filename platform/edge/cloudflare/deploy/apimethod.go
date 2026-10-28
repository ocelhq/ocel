package cloudflare

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/pkg/tracing"
	"go.opentelemetry.io/otel/attribute"
)

const (
	apiSystem  = "cloudflare-api"
	apiService = "cloudflare"
	apiPrefix  = "/client/v4"
)

var (
	versionSegment = regexp.MustCompile(`^v[0-9]+$`)
	generatedID    = regexp.MustCompile(`^[0-9a-fA-F-]{16,}$`)
)

var collectionsAddressedByName = map[string]bool{
	"accounts": true,
	"scripts":  true,
	"secrets":  true,
	"settings": true,
	"values":   true,
	"zones":    true,
}

func nameAPIMethod(req *http.Request) (string, []attribute.KeyValue, bool) {
	words := resourceWords(req.URL.EscapedPath())
	if len(words) == 0 {
		return "", nil, false
	}
	name, attrs := tracing.NameRPCSpan(apiSystem, apiService, strings.Join(append(words, strings.ToLower(req.Method)), "."))
	return name, attrs, true
}

func resourceWords(path string) []string {
	if rest, ok := strings.CutPrefix(path, apiPrefix); ok && (rest == "" || rest[0] == '/') {
		path = rest
	}
	var words []string
	previous := ""
	for _, segment := range strings.Split(path, "/") {
		if segment != "" && !isIdentifier(segment, previous) && !versionSegment.MatchString(segment) {
			words = append(words, segment)
		}
		previous = segment
	}
	return words
}

func isIdentifier(segment, previous string) bool {
	return collectionsAddressedByName[previous] || strings.Contains(segment, ".") || generatedID.MatchString(segment)
}

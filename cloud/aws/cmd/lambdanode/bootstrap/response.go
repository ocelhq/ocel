package main

import (
	"encoding/json"
	"net/http"
)

const (
	contentTypeHTTPIntegration = "application/vnd.awslambda.http-integration-response"
	headerResponseMode         = "Lambda-Runtime-Function-Response-Mode"
	responseModeStreaming      = "streaming"
	headerErrorType            = "Lambda-Runtime-Function-Error-Type"
	headerErrorBody            = "Lambda-Runtime-Function-Error-Body"
	preludeSeparatorLen        = 8

	errTypeUpstream = "Ocel.UpstreamError"

	emptyBodyHeader   = "X-Ocel-Empty-Body"
	emptyBodySentinel = "\n"
)

type prelude struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Cookies    []string          `json:"cookies"`
}

func encodePrelude(status int, header http.Header) ([]byte, error) {
	p := prelude{
		StatusCode: status,
		Headers:    flattenHeaders(header),
		Cookies:    header.Values("Set-Cookie"),
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return append(b, make([]byte, preludeSeparatorLen)...), nil
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k := range h {
		if http.CanonicalHeaderKey(k) == "Set-Cookie" {
			continue
		}
		out[k] = h.Get(k)
	}
	return out
}

package main

import (
	"encoding/json"
	"net/http"
)

// Streaming response wire constants for the Lambda Runtime API. A streaming
// response POSTs to /response with this content type; the body is a JSON
// prelude carrying status/headers, then preludeSeparatorLen null bytes, then
// the raw response body streamed as it arrives.
const (
	contentTypeHTTPIntegration = "application/vnd.awslambda.http-integration-response"
	headerResponseMode         = "Lambda-Runtime-Function-Response-Mode"
	responseModeStreaming      = "streaming"
	headerErrorType            = "Lambda-Runtime-Function-Error-Type"
	headerErrorBody            = "Lambda-Runtime-Function-Error-Body"
	preludeSeparatorLen        = 8

	// errTypeUpstream is the error type reported when the user's app (Node)
	// fails a request — before the response starts (502 prelude) or mid-stream
	// (error trailer).
	errTypeUpstream = "Ocel.UpstreamError"
)

// prelude is the JSON header block of an http-integration-response.
type prelude struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Cookies    []string          `json:"cookies"`
}

// encodePrelude builds the streaming-response prelude: the JSON status/header
// block followed by preludeSeparatorLen null bytes. Set-Cookie response headers
// are lifted into the cookies array (Function URLs surface them there, not in
// the headers map).
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

// flattenHeaders collapses an http.Header to single values, dropping Set-Cookie
// (carried in the prelude's cookies array instead).
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

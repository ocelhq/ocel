// cmd/bootstrap/forward.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-lambda-go/lambdacontext"
)

// LambdaEvent is the incoming event shape (API Gateway / Ocel proxy style).
type LambdaEvent struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// ProxyResponse is what we return to Lambda (serialized to the response bytes).
type ProxyResponse struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
}

// forward turns the Lambda event into a real HTTP request against the user's
// app on loopback, and turns the HTTP response back into ProxyResponse bytes.
func (m *Membrane) forward(ctx context.Context, lc *lambdacontext.LambdaContext, payload []byte) ([]byte, error) {
	var event LambdaEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("bad event payload: %w", err)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d%s", m.nodePort, event.Path)
	req, err := http.NewRequestWithContext(ctx, event.Method, url, strings.NewReader(event.Body))
	if err != nil {
		return nil, err
	}
	for k, v := range event.Headers {
		req.Header.Set(k, v)
	}

	// Inject internal context the JS wrapper reads per-request (and strips
	// before the user's app sees it).
	if lc != nil {
		req.Header.Set("x-ocel-request-id", lc.AwsRequestID)
		req.Header.Set("x-ocel-function-arn", lc.InvokedFunctionArn)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(ProxyResponse{
		StatusCode: resp.StatusCode,
		Headers:    flattenHeaders(resp.Header),
		Body:       string(body),
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k := range h {
		out[k] = h.Get(k) // first value; extend for multi-value if needed
	}
	return out
}

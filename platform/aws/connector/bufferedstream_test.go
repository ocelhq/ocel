package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/aws/aws-lambda-go/events"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type streamingRollback struct {
	contractv1connect.UnimplementedProviderServiceHandler
}

func (streamingRollback) Rollback(_ context.Context, _ *contractv1.RollbackRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	for range 2 {
		if err := stream.Send(&progressv1.OperationEvent{}); err != nil {
			return err
		}
	}
	return nil
}

type functionURL struct{ adapter *httpadapter.HandlerAdapterV2 }

func (f functionURL) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{}
	for key := range req.Header {
		headers[key] = req.Header.Get(key)
	}
	event := events.APIGatewayV2HTTPRequest{
		RawPath:         req.URL.Path,
		Headers:         headers,
		Body:            base64.StdEncoding.EncodeToString(body),
		IsBase64Encoded: true,
		RequestContext:  events.APIGatewayV2HTTPRequestContext{HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: req.Method, Path: req.URL.Path}},
	}
	answered, err := f.adapter.ProxyWithContext(req.Context(), event)
	if err != nil {
		return nil, err
	}
	responseBody := []byte(answered.Body)
	if answered.IsBase64Encoded {
		if responseBody, err = base64.StdEncoding.DecodeString(answered.Body); err != nil {
			return nil, err
		}
	}
	response := &http.Response{StatusCode: answered.StatusCode, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(responseBody)), Request: req}
	for key, value := range answered.Headers {
		response.Header.Set(key, value)
	}
	return response, nil
}

func TestAServerStreamThroughTheFunctionUrlArrivesWhole(t *testing.T) {
	t.Parallel()

	path, handler := contractv1connect.NewProviderServiceHandler(streamingRollback{})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	client := contractv1connect.NewProviderServiceClient(&http.Client{Transport: functionURL{adapter: httpadapter.NewV2(bufferingStreams(mux))}}, "https://connector.example.com")

	stream, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{})
	if err != nil {
		t.Fatalf("Rollback() = %v", err)
	}
	received := 0
	for stream.Receive() {
		received++
	}
	if err := stream.Err(); err != nil || received != 2 {
		t.Errorf("the rollback stream through the function url carried %d events and ended with %v, want both events: a Lambda response is buffered, so the stream arrives whole when the call ends", received, err)
	}
}

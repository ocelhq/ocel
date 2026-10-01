package infra

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protowire"
)

const runtimeAddressEnv = "OCEL_RUNTIME_ADDRESS"

var recordedProcedures = []string{"/Trigger", "/Send"}

type SentRequest struct {
	Procedure string `json:"procedure"`
	Name      string `json:"name"`
	Payload   string `json:"payload"`
}

var sent struct {
	sync.Mutex
	requests []SentRequest
}

func recordRequests() error {
	address := os.Getenv(runtimeAddressEnv)
	if address == "" {
		return nil
	}
	runtime, err := url.Parse(address)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(runtime) }}
	go func() {
		_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			if isRecorded(r.URL.Path) {
				appendRequest(decodeRequest(r.URL.Path, r.Header.Get("Content-Type"), body))
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			proxy.ServeHTTP(w, r)
		}))
	}()
	return os.Setenv(runtimeAddressEnv, "http://"+listener.Addr().String())
}

func isRecorded(path string) bool {
	for _, procedure := range recordedProcedures {
		if strings.HasSuffix(path, procedure) {
			return true
		}
	}
	return false
}

func appendRequest(request SentRequest) {
	sent.Lock()
	defer sent.Unlock()
	sent.requests = append(sent.requests, request)
}

func CountRequests() int {
	sent.Lock()
	defer sent.Unlock()
	return len(sent.requests)
}

func ListRequestsSince(n int) []SentRequest {
	sent.Lock()
	defer sent.Unlock()
	return append([]SentRequest{}, sent.requests[n:]...)
}

func decodeRequest(procedure, contentType string, body []byte) SentRequest {
	request := SentRequest{Procedure: procedure}
	switch contentType {
	case "application/proto":
		for len(body) > 0 {
			number, kind, n := protowire.ConsumeTag(body)
			if n < 0 {
				request.Payload = fmt.Sprintf("the request is no protobuf message: %v", protowire.ParseError(n))
				return request
			}
			body = body[n:]
			if kind != protowire.BytesType {
				n = protowire.ConsumeFieldValue(number, kind, body)
				body = body[max(n, 0):]
				continue
			}
			value, n := protowire.ConsumeBytes(body)
			body = body[max(n, 0):]
			switch number {
			case 1:
				request.Name = string(value)
			case 2:
				request.Payload = string(value)
			}
		}
	default:
		request.Payload = fmt.Sprintf("the request is %s, which this recorder does not read", contentType)
	}
	return request
}

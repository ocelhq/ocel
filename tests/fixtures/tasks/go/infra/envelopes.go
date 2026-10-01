package infra

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"time"

	"ocel.dev"
)

const (
	workerEnv          = "OCEL_WORKER"
	loopback           = "127.0.0.1"
	listensWithin      = 30 * time.Second
	listenPollInterval = 50 * time.Millisecond
)

func recordEnvelopes() error {
	if os.Getenv(workerEnv) == "" {
		return nil
	}
	front, err := net.Listen("tcp", net.JoinHostPort(cmp.Or(os.Getenv("HOST"), loopback), os.Getenv("PORT")))
	if err != nil {
		return err
	}
	port, err := findFreePort()
	if err != nil {
		return err
	}
	if err := os.Setenv("HOST", loopback); err != nil {
		return err
	}
	if err := os.Setenv("PORT", strconv.Itoa(port)); err != nil {
		return err
	}
	sdk := &url.URL{Scheme: "http", Host: net.JoinHostPort(loopback, strconv.Itoa(port))}
	proxy := &httputil.ReverseProxy{
		Rewrite:   func(r *httputil.ProxyRequest) { r.SetURL(sdk) },
		Transport: &http.Transport{DialContext: dialOnceListening},
	}
	go func() {
		_ = http.Serve(front, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			if err := recordEnvelope(r.Context(), body); err != nil {
				http.Error(w, "record the envelope: "+err.Error(), http.StatusBadGateway)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			proxy.ServeHTTP(w, r)
		}))
	}()
	return nil
}

func findFreePort() (int, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(loopback, "0"))
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func dialOnceListening(ctx context.Context, network, address string) (net.Conn, error) {
	deadline := time.Now().Add(listensWithin)
	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err == nil || time.Now().After(deadline) || ctx.Err() != nil {
			return conn, err
		}
		time.Sleep(listenPollInterval)
	}
}

type RecordedEnvelope struct {
	RecordedEnvelope string `json:"recordedEnvelope"`
}

var EnvelopeTask = ocel.Task("envelope", func(_ context.Context, envelope RecordedEnvelope) (RecordedEnvelope, error) {
	return envelope, nil
})

func recordEnvelope(ctx context.Context, body []byte) error {
	var envelope struct {
		Execution string              `json:"execution"`
		Message   struct{ ID string } `json:"message"`
		Payload   json.RawMessage     `json:"payload"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	var own RecordedEnvelope
	if json.Unmarshal(envelope.Payload, &own) == nil && own.RecordedEnvelope != "" {
		return nil
	}
	var tags []string
	for _, tag := range []string{envelope.Execution, envelope.Message.ID} {
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	_, err := EnvelopeTask.Trigger(ctx, RecordedEnvelope{RecordedEnvelope: string(body)}, ocel.Tags(tags...))
	return err
}

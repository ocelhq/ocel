package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type child interface {
	endpoint() upstream
}

type controlledChild interface {
	refreshLiveValues(ctx context.Context)
	beginInvocation(requestID string) <-chan struct{}
	endInvocation(ctx context.Context, requestID string, waiter <-chan struct{}, reached bool)
	answerWarmInvocation(ctx context.Context, rw *responseWriter) error
}

type upstream struct {
	port   int
	client *http.Client
}

func (u upstream) endpoint() upstream { return u }

func newLoopbackClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			MaxIdleConns:        16,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     4 * time.Second,
		},
	}
}

func supervise(what string, exited <-chan error) {
	err := <-exited
	fmt.Fprintf(os.Stderr, "ocel: %s exited after startup: %v\n", what, err)
	os.Exit(1)
}

func executable(a providerkit.FunctionConfig) bool { return len(a.Command) > 0 }

func readArtifact() providerkit.FunctionConfig {
	var a providerkit.FunctionConfig
	data, err := os.ReadFile(filepath.Join(taskRoot(), providerkit.FunctionConfigFile))
	if err != nil {
		return a
	}
	if json.Unmarshal(data, &a) != nil {
		return providerkit.FunctionConfig{}
	}
	return a
}

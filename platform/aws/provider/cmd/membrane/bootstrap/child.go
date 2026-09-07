package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type child interface {
	endpoint() upstream
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

type artifact struct {
	Runtime struct {
		Name string `json:"name"`
	} `json:"runtime"`
	Command []string `json:"command"`
}

func (a artifact) executable() bool { return len(a.Command) > 0 }

func readArtifact() artifact {
	var a artifact
	data, err := os.ReadFile(filepath.Join(taskRoot(), "config.json"))
	if err != nil {
		return a
	}
	if json.Unmarshal(data, &a) != nil {
		return artifact{}
	}
	return a
}

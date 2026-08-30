package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	socketEnv     = "OCEL_PROXY_ADMIN"
	defaultSocket = "/run/caddy-admin.sock"
)

const (
	exitRefused   = 2
	exitUnhealthy = 3
	exitSilent    = 4
)

const (
	gateInterval = 250 * time.Millisecond
	gateAttempt  = 2 * time.Second
)

const (
	loadPath      = "/load"
	upstreamsPath = "/reverse_proxy/upstreams"
	configPath    = "/config/"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(argv []string, out, errs io.Writer) int {
	socket := os.Getenv(socketEnv)
	if socket == "" {
		socket = defaultSocket
	}
	if len(argv) == 0 {
		return usage(errs)
	}
	rest := argv[1:]
	switch argv[0] {
	case "gate":
		if len(rest) != 3 {
			return usage(errs)
		}
		return gate(rest[0], rest[1], rest[2], out, errs)
	case "flip":
		if len(rest) != 1 {
			return usage(errs)
		}
		return flip(socket, rest[0], out, errs)
	case "upstreams":
		if len(rest) != 0 {
			return usage(errs)
		}
		return ask(socket, upstreamsPath, out, errs)
	case "config":
		if len(rest) != 1 {
			return usage(errs)
		}
		return ask(socket, configPath+strings.TrimPrefix(rest[0], "/"), out, errs)
	default:
		return usage(errs)
	}
}

func usage(errs io.Writer) int {
	fmt.Fprintln(errs, "usage: ocel-proxyctl gate <host:port> <path> <seconds> | flip <config> | upstreams | config <path>")
	return exitRefused
}

func gate(target, path, window string, out, errs io.Writer) int {
	seconds, err := strconv.Atoi(window)
	if err != nil || seconds <= 0 {
		fmt.Fprintf(errs, "ocel-proxyctl: %q is no number of seconds to wait\n", window)
		return exitRefused
	}
	return gating(target, path, time.Duration(seconds)*time.Second, out, errs)
}

func gating(target, path string, window time.Duration, out, errs io.Writer) int {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	attempt := min(window, gateAttempt)
	client := &http.Client{Timeout: attempt}
	deadline := time.Now().Add(window)
	status := 0
	for {
		answer, err := client.Get("http://" + target + path)
		if err == nil {
			status = answer.StatusCode
			_, _ = io.Copy(io.Discard, answer.Body)
			_ = answer.Body.Close()
			if status/100 == 2 {
				fmt.Fprintln(out, status)
				return 0
			}
		}
		if time.Now().Add(gateInterval).After(deadline) {
			break
		}
		time.Sleep(gateInterval)
	}
	if status == 0 {
		fmt.Fprintf(errs, "%s never answered %s within %s\n", target, path, window)
		return exitSilent
	}
	fmt.Fprintln(out, status)
	fmt.Fprintf(errs, "%s answered %s with status %d and never with a 2xx within %s\n", target, path, status, window)
	return exitUnhealthy
}

func flip(socket, path string, out, errs io.Writer) int {
	document, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %s is no config this host can read: %v\n", path, err)
		return exitRefused
	}
	if err := keepsAdmin(document, socket); err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %s %v\n", path, err)
		return exitRefused
	}
	return speak(socket, http.MethodPost, loadPath, document, out, errs)
}

func keepsAdmin(document []byte, socket string) error {
	var read struct {
		Admin *struct {
			Disabled bool   `json:"disabled"`
			Listen   string `json:"listen"`
		} `json:"admin"`
	}
	if err := json.Unmarshal(document, &read); err != nil {
		return fmt.Errorf("is not json a proxy could load: %w", err)
	}
	wanted := "unix/" + socket
	moving := errors.New("declares no admin endpoint at " + wanted +
		", and caddy moves the admin endpoint before it validates the rest: a config without one takes the socket this helper is reached through with it and opens a tcp listener in its place")
	if read.Admin == nil || read.Admin.Disabled {
		return moving
	}
	if listen, _, _ := strings.Cut(read.Admin.Listen, "|"); listen != wanted {
		return moving
	}
	return nil
}

func ask(socket, path string, out, errs io.Writer) int {
	return speak(socket, http.MethodGet, path, nil, out, errs)
}

func speak(socket, method, path string, body []byte, out, errs io.Writer) int {
	request, err := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
		return exitRefused
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	answer, err := client.Do(request)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy answered nothing over %s: %v\n", socket, err)
		return exitRefused
	}
	defer answer.Body.Close()
	said, err := io.ReadAll(answer.Body)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy cut its answer to %s short: %v\n", path, err)
		return exitRefused
	}
	if answer.StatusCode/100 != 2 {
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy answered %s with %s: %s\n", path, answer.Status, strings.TrimSpace(string(said)))
		return exitRefused
	}
	if len(said) > 0 {
		_, _ = out.Write(said)
		if said[len(said)-1] != '\n' {
			fmt.Fprintln(out)
		}
	}
	return 0
}

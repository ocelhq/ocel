package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	holdsTimeout = 5 * time.Second
	holdsCap     = 1 << 20
)

func holds(argv []string, errs io.Writer) int {
	if len(argv) != 2 || argv[0] == "" || !strings.HasPrefix(argv[1], "/") {
		return usage(errs)
	}
	socket, path := argv[0], argv[1]
	dialer := &net.Dialer{}
	client := &http.Client{Timeout: holdsTimeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
	}}
	said, err := client.Get("http://" + switchboard.Name + path)
	if err != nil {
		fmt.Fprintf(errs, "%s: nothing answered on %s: %v\n", switchboard.Name, socket, err)
		return exitNotServingYet
	}
	defer said.Body.Close()
	body, err := io.ReadAll(io.LimitReader(said.Body, holdsCap))
	switch trimmed := bytes.TrimSpace(body); {
	case err != nil:
		fmt.Fprintf(errs, "%s: %s answered %s and the answer was cut short: %v\n", switchboard.Name, socket, path, err)
	case said.StatusCode != http.StatusOK:
		fmt.Fprintf(errs, "%s: %s answered %d for %s: %s\n", switchboard.Name, socket, said.StatusCode, path, trimmed)
	case len(trimmed) == 0 || string(trimmed) == "null":
		fmt.Fprintf(errs, "%s: %s holds nothing at %s\n", switchboard.Name, socket, path)
	default:
		return 0
	}
	return exitNotServingYet
}

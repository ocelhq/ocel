package main

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"
)

type Membrane struct {
	nodePort int
	control  net.Conn
	client   *http.Client
}

type controlMsg struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}
type serverReadyPayload struct {
	HTTPPort int `json:"httpPort"`
}

func startNode() (*Membrane, error) {
	// TODO: randomize
	sockPath := "/tmp/ocel-control.sock"
	_ = os.Remove(sockPath) // stale socket from a reused sandbox

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("/var/lang/bin/node", "/opt/ocel/node-entrypoint.mjs")
	cmd.Env = append(os.Environ(),
		"OCEL_CONTROL_SOCKET="+sockPath,
		"OCEL_HANDLER="+os.Getenv("OCEL_HANDLER"), // user's compiled entry
	)
	cmd.Stdout = os.Stdout // Node stdout → CloudWatch
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Node connects back to our control socket.
	control, err := ln.Accept()
	if err != nil {
		return nil, err
	}

	m := &Membrane{control: control}

	// Read control messages until "server-ready" gives us the port.
	reader := bufio.NewReader(control)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var msg controlMsg
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		if msg.Type == "server-ready" {
			var p serverReadyPayload
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				return nil, err
			}
			m.nodePort = p.HTTPPort
			break
		}
	}

	// Data-plane client: plain loopback TCP. Tune the transport for the
	// single-client, keep-alive-to-one-host case.
	m.client = &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        16,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	// Keep draining control messages (logs, metrics, completion) in the background.
	go m.drainControl(reader)
	return m, nil
}

func (m *Membrane) drainControl(reader *bufio.Reader) {
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return // Node died; sandbox will be recycled by Lambda
		}
		var msg controlMsg
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "log":
			// forward to Ocel's log pipeline
		case "metric":
			// forward to Ocel telemetry
		case "request-end":
			// per-request completion signal from the JS wrapper
		}
	}
}

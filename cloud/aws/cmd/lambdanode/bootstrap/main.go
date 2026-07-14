package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

func main() {
	if _, err := os.Stat("/opt/ocel/node-entrypoint.mjs"); err != nil {
		fatalInit(fmt.Sprintf("node-entrypoint.mjs not found: %v", err))
	}
	if _, err := os.Stat("/var/lang/bin/node"); err != nil {
		fatalInit(fmt.Sprintf("node binary not found: %v", err))
	}

	membrane, err := startNode()
	if err != nil {
		// Must report init failure BEFORE we start polling the Runtime API.
		fatalInit(fmt.Sprintf("failed to start node runtime: %v", err))
	}

	rt := newRuntimeClient(os.Getenv("AWS_LAMBDA_RUNTIME_API"))
	ctx := context.Background()
	for {
		if err := handleInvocation(ctx, rt, membrane); err != nil {
			// A Runtime API failure is fatal to the loop; the sandbox is recycled.
			fmt.Fprintf(os.Stderr, "ocel: runtime loop error: %v\n", err)
			os.Exit(1)
		}
	}
}

func fatalInit(msg string) {
	api := os.Getenv("AWS_LAMBDA_RUNTIME_API")
	if api != "" {
		url := "http://" + api + "/2018-06-01/runtime/init/error"
		payload, _ := json.Marshal(map[string]string{
			"errorMessage": msg,
			"errorType":    "Ocel.InitError",
		})
		req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
		req.Header.Set("Lambda-Runtime-Function-Error-Type", "Ocel.InitError")
		http.DefaultClient.Do(req)
	}
	os.Exit(1)
}

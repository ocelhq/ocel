package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-lambda-go/lambdacontext"
)

func main() {
	if _, err := os.Stat("/opt/ocel/runtime.mjs"); err != nil {
		fatalInit(fmt.Sprintf("runtime.mjs not found: %v", err))
	}
	if _, err := os.Stat("/var/lang/bin/node"); err != nil {
		fatalInit(fmt.Sprintf("node binary not found: %v", err))
	}

	membrane, err := startNode()
	if err != nil {
		// Must report init failure BEFORE lambda.Start takes over the loop.
		fatalInit(fmt.Sprintf("failed to start node runtime: %v", err))
	}

	lambda.Start(membrane)
}

func (m *Membrane) Invoke(ctx context.Context, payload []byte) ([]byte, error) {
	lc, _ := lambdacontext.FromContext(ctx)

	// TODO: ocel pre-invoke logic

	resp, err := m.forward(ctx, lc, payload)
	if err != nil {
		return nil, err
	}

	// TODO: post invoke logic

	return resp, nil
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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/processenv"
	process "github.com/ocelhq/ocel/pkg/runtime/child"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/runtime/tasks"
)

const workerStartBudget = 30 * time.Second

type workerEvent struct {
	Records       []events.SQSMessage `json:"Records"`
	Cron          string              `json:"ocelCron"`
	ScheduledTime string              `json:"scheduledTime"`
}

func workerCommand(declared []string) []string {
	command := append([]string(nil), declared...)
	if len(command) > 0 && command[0] == "node" {
		command[0] = nodeBinaryPath
	}
	return command
}

func runWorker(ctx context.Context, worker string, served buildoutput.FunctionConfig, values liveValues, prefetch <-chan error, bakedEnv []string, cfg proxyConfig) {
	if len(served.Worker) == 0 {
		fatalInit(fmt.Sprintf("this function runs worker %q, and its app's build carries no worker entry to start", worker))
	}
	if cfg.queues == nil {
		fatalInit(fmt.Sprintf("this function runs worker %q, and its code carries no queue topology naming what it serves", worker))
	}
	if declared := os.Getenv(queues.WorkerConcurrencyEnv); declared != "" {
		concurrency, err := strconv.Atoi(declared)
		if err != nil || concurrency < 1 {
			fatalInit(fmt.Sprintf("%s=%q is not a concurrency of at least 1", queues.WorkerConcurrencyEnv, declared))
		}
		if cfg.queues.Workers == nil {
			cfg.queues.Workers = map[string]queues.Worker{}
		}
		cfg.queues.Workers[worker] = queues.Worker{Concurrency: concurrency}
	}
	port, err := process.FreePort()
	if err != nil {
		fatalInit(fmt.Sprintf("find a loopback port for worker %q: %v", worker, err))
	}
	address := "127.0.0.1:" + strconv.Itoa(port)
	cfg.worker, cfg.workerURL = worker, "http://"+address
	bound, err := serveProxy(ctx, values, cfg)
	if err != nil {
		fatalInit(fmt.Sprintf("failed to serve this deployment's proxied bindings: %v", err))
	}
	go superviseProxy(bound.errs)
	if err := values.Join(prefetch); err != nil {
		fatalInit(fmt.Sprintf("failed to resolve this deployment's live variables: %v", err))
	}

	env := append(os.Environ(), "PORT="+strconv.Itoa(port), "HOST=127.0.0.1", processenv.WorkerEnvVar+"="+worker)
	env = append(env, childEnv(bakedEnv, values, bound.env)...)
	command := workerCommand(served.Worker)
	proc, err := process.Start(process.Options{Command: command, Dir: taskRoot(), Env: env, Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		fatalInit(fmt.Sprintf("start worker %q: %v", worker, err))
	}
	select {
	case err := <-process.WatchListening(address, proc.Exited()):
		if err != nil {
			fatalInit(fmt.Sprintf("worker %q: %v", worker, err))
		}
	case <-time.After(workerStartBudget):
		fatalInit(fmt.Sprintf("worker %q did not listen on %s within %v", worker, address, workerStartBudget))
	}
	go func() {
		exit := <-proc.Exited()
		fmt.Fprintf(os.Stderr, "ocel: worker %q exited after startup: %v\n", worker, exit)
		os.Exit(1)
	}()

	rt := newRuntimeClient(os.Getenv("AWS_LAMBDA_RUNTIME_API"))
	for {
		inv, err := rt.next(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ocel: runtime loop error: %v\n", err)
			os.Exit(1)
		}
		answerWorkerInvocation(ctx, rt, inv, bound.engine)
	}
}

func answerWorkerInvocation(ctx context.Context, rt *runtimeClient, inv *invocation, engine *tasks.Engine) {
	if inv.deadlineMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.UnixMilli(inv.deadlineMs))
		defer cancel()
	}
	answer, err := workerAnswer(ctx, inv.Payload, engine)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: %s: %v\n", inv.lc.AwsRequestID, err)
		if err := rt.reportError(ctx, inv.lc.AwsRequestID, "Ocel.WorkerError", err.Error()); err != nil {
			fmt.Fprintf(os.Stderr, "ocel: report the failure of %s: %v\n", inv.lc.AwsRequestID, err)
		}
		return
	}
	if err := rt.respond(ctx, inv.lc.AwsRequestID, answer); err != nil {
		fmt.Fprintf(os.Stderr, "ocel: deliver response for %s: %v\n", inv.lc.AwsRequestID, err)
	}
}

func workerAnswer(ctx context.Context, payload []byte, engine *tasks.Engine) ([]byte, error) {
	if isWarmInvocation(payload) {
		return []byte(`{}`), nil
	}
	var event workerEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("the worker was invoked with an event it does not read: %w", err)
	}
	switch {
	case event.Cron != "":
		scheduled, err := time.Parse(time.RFC3339, event.ScheduledTime)
		if err != nil {
			scheduled = time.Now()
		}
		if err := engine.FireCron(ctx, event.Cron, scheduled); err != nil {
			return nil, err
		}
		return []byte(`{}`), nil
	case len(event.Records) > 0:
		return json.Marshal(engine.Deliver(ctx, events.SQSEvent{Records: event.Records}))
	}
	return nil, fmt.Errorf("the worker was invoked with neither queue records nor a schedule")
}

func (c *runtimeClient) respond(ctx context.Context, requestID string, body []byte) error {
	return c.post(ctx, "/invocation/"+requestID+"/response", body, nil)
}

func (c *runtimeClient) reportError(ctx context.Context, requestID, errType, message string) error {
	body, err := json.Marshal(map[string]string{"errorType": errType, "errorMessage": message})
	if err != nil {
		return err
	}
	return c.post(ctx, "/invocation/"+requestID+"/error", body, map[string]string{headerErrorType: errType})
}

func (c *runtimeClient) post(ctx context.Context, path string, body []byte, header map[string]string) error {
	req, err := http.NewRequestWithContext(context.WithoutCancel(ctx), http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range header {
		req.Header.Set(name, value)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("the runtime api answered %s", resp.Status)
	}
	return nil
}

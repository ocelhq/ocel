package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/ocelhq/ocel/pkg/runtime/child"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/runtime/tasks"
)

var consumeQueues = func(ctx context.Context, worker, workerURL string, topology queues.Topology) error {
	aws, err := sdkconfig.Workload(ctx)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}
	return tasks.NewConsumption(tasks.NewAWS(aws, topology, worker, workerURL), sqs.NewFromConfig(aws)).Run(ctx)
}

func readTopology(raw string) (queues.Topology, error) {
	if raw == "" {
		return queues.Topology{}, fmt.Errorf("this container runs a worker, and its task definition names no %s for it to serve", queues.EnvVar)
	}
	return queues.Parse([]byte(raw))
}

func serveWorker(ctx context.Context, worker string, topology queues.Topology, proc *child.Process, port int) int {
	address := "127.0.0.1:" + strconv.Itoa(port)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, forwarded...)
	defer signal.Stop(signals)

	listening := child.WatchListening(address, nil)
	polling, stopPolling := context.WithCancel(ctx)
	defer stopPolling()
	var stopped chan error

	for {
		select {
		case <-listening:
			listening = nil
			stopped = make(chan error, 1)
			go func() { stopped <- consumeQueues(polling, worker, "http://"+address, topology) }()
		case sig := <-signals:
			fmt.Fprintf(os.Stderr, forwardedSignal, sig)
			stopPolling()
			if stopped != nil {
				<-stopped
			}
			return exitCode(proc.Stop(stopGrace))
		case err := <-stopped:
			stopped = nil
			if err != nil {
				fmt.Fprintf(os.Stderr, "ocel: worker %q stopped polling its queues: %v\n", worker, err)
				_ = proc.Stop(stopGrace)
				return 1
			}
		case exit := <-proc.Exited():
			if exit.Err != nil {
				fmt.Fprintf(os.Stderr, "ocel: the worker entry (%s) exited: %v\n", proc.Program(), exit.Err)
			}
			stopPolling()
			if stopped != nil {
				<-stopped
			}
			return exitCode(exit)
		}
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

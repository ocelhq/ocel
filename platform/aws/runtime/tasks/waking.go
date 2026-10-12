package tasks

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

const (
	wakeLead     = time.Minute
	wakeInterval = 30 * time.Second
)

type serviceControl interface {
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, optFns ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	UpdateService(ctx context.Context, in *ecs.UpdateServiceInput, optFns ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
}

func (e *Engine) wake(ctx context.Context, worker string, delay time.Duration) {
	service := e.cfg.Topology.Workers[worker].Service
	if service == nil || e.cfg.Services == nil || delay > wakeLead || !e.dueForWake(worker) {
		return
	}
	described, err := e.cfg.Services.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(service.Cluster), Services: []string{service.Name}})
	if err != nil || len(described.Services) != 1 {
		slog.Warn("read the service a message is waiting for", "worker", worker, "error", err)
		return
	}
	if described.Services[0].DesiredCount > 0 {
		return
	}
	if _, err := e.cfg.Services.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: aws.String(service.Cluster), Service: aws.String(service.Name), DesiredCount: aws.Int32(1)}); err != nil {
		slog.Warn("start the worker a message is waiting for", "worker", worker, "error", err)
	}
}

func (e *Engine) dueForWake(worker string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if last, woken := e.woken[worker]; woken && time.Since(last) < wakeInterval {
		return false
	}
	e.woken[worker] = time.Now()
	return true
}

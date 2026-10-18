package tasks

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const (
	wakeLead     = time.Minute
	wakeInterval = 30 * time.Second
)

type serviceControl interface {
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, optFns ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	UpdateService(ctx context.Context, in *ecs.UpdateServiceInput, optFns ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
}

type workerServices struct {
	workers map[string]queues.Worker
	ecs     serviceControl

	mu    sync.Mutex
	woken map[string]time.Time
}

func newWorkerServices(workers map[string]queues.Worker, control serviceControl) *workerServices {
	return &workerServices{workers: workers, ecs: control, woken: map[string]time.Time{}}
}

func (s *workerServices) serviceOf(worker string) (*queues.Service, bool) {
	service := s.workers[worker].Service
	return service, service != nil && s.ecs != nil
}

func (s *workerServices) wake(ctx context.Context, worker string, delay time.Duration) {
	service, scales := s.serviceOf(worker)
	if !scales || delay > wakeLead || !s.dueForWake(worker) {
		return
	}
	described, err := s.ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(service.Cluster), Services: []string{service.Name}})
	if err != nil || len(described.Services) != 1 {
		slog.Warn("read the service a message is waiting for", "worker", worker, "error", err)
		return
	}
	if described.Services[0].DesiredCount > 0 {
		return
	}
	if _, err := s.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: aws.String(service.Cluster), Service: aws.String(service.Name), DesiredCount: aws.Int32(1)}); err != nil {
		slog.Warn("start the worker a message is waiting for", "worker", worker, "error", err)
	}
}

func (s *workerServices) dueForWake(worker string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, woken := s.woken[worker]; woken && time.Since(last) < wakeInterval {
		return false
	}
	s.woken[worker] = time.Now()
	return true
}

func (s *workerServices) scaleInToFloor(ctx context.Context, worker string) error {
	service, _ := s.serviceOf(worker)
	_, err := s.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: aws.String(service.Cluster), Service: aws.String(service.Name), DesiredCount: aws.Int32(int32(service.Floor))})
	return err
}

package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

type runnerRole interface {
	Read(ctx context.Context, settings host.RunnerSettings) (host.RunnerReading, error)
	Apply(ctx context.Context, settings host.RunnerSettings, read host.RunnerReading, progress progress.Log) error
	Remove(ctx context.Context, progress progress.Log) error
}

type connectorJobs struct {
	runner runnerRole
	arch   func(ctx context.Context) (string, error)
}

func (j connectorJobs) Reconcile(ctx context.Context, hostname, version string, asked *provider.ConnectorRunner, progress progress.Log) error {
	if asked == nil {
		return j.Remove(ctx, progress)
	}
	settings := host.RunnerSettings{
		URL:     "https://" + hostname + boxapi.ConnectorPath,
		Version: provider.WrittenBy(version),
		Pulled:  asked.Image,
	}
	if err := host.RefuseUnbuildableJobImage(settings); err != nil {
		return err
	}
	arch, err := j.arch(ctx)
	if err != nil {
		return err
	}
	settings.Arch = arch
	read, err := j.runner.Read(ctx, settings)
	if err != nil {
		return err
	}
	settings.CPUs, settings.Memory = read.ChooseCaps(asked.CPUs, asked.Memory)
	return j.runner.Apply(ctx, settings, read, progress)
}

func (j connectorJobs) Remove(ctx context.Context, progress progress.Log) error {
	read, err := j.runner.Read(ctx, host.RunnerSettings{})
	if err != nil || !read.HasRunner() {
		return err
	}
	return j.runner.Remove(ctx, progress)
}

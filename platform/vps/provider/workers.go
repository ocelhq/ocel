package vps

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func workerCommand(app *provider.AppSpec) ([]string, error) {
	if len(app.Workers) == 0 {
		return nil, nil
	}
	command, runs := containerimage.WorkerCommand(app.Framework)
	if !runs {
		return nil, refusal.Refuse(refusal.CodeUnsupported,
			"worker %q joins app %s, a %s app, and ocel builds no worker entry into a %s image: give the worker an app of a framework that runs one (node, next, go or rust)",
			app.Workers[0].Name, app.App, app.Framework, app.Framework)
	}
	return command, nil
}

func usesQueue(app *provider.AppSpec) bool {
	return len(app.Workers) > 0 || slices.ContainsFunc(app.Values.Bindings, func(binding provider.Binding) bool {
		return binding.Type == provider.BindingTopic || binding.Type == provider.BindingTask
	})
}

func workerEnv(app *provider.AppSpec, worker string) map[string]string {
	env := maps.Clone(app.Values.ContainerEnv)
	if env == nil {
		env = map[string]string{}
	}
	delete(env, originguard.OriginSecretVar)
	delete(env, originguard.OriginSecretPreviousVar)
	env[processenv.WorkerEnvVar] = worker
	return env
}

func (p *Provider) runWorkers(ctx context.Context, spec provider.StackSpec, command []string, manifest []byte, progress progress.Log) error {
	app, ref := spec.App, spec.Ref
	declared := make([]string, 0, len(app.Workers))
	for _, worker := range app.Workers {
		declared = append(declared, worker.Name)
		name := host.ContainerName(ref.Name.String(), app.App+"-"+worker.Name, app.Deployment, app.Image)
		if progress != nil {
			progress.Say("Starting worker " + worker.Name + " from " + app.App + "'s image as " + name)
		}
		if err := p.host.RunContainer(ctx, host.Container{
			Name: name, Project: ref.Project, App: app.App, Image: app.Image, Tier: ref.Tier,
			Env: workerEnv(app, worker.Name), Manifest: manifest, Resolved: true, Command: command,
		}); err != nil {
			return err
		}
		replaced, err := p.recordWorker(ctx, ref, worker.Name, live.QueueWorker{
			App: app.App, Stack: ref.Name.String(), Container: name, Concurrency: worker.Concurrency,
		})
		if err != nil {
			return err
		}
		if replaced != "" && replaced != name {
			if err := p.host.TakeDown(ctx, ref.Tier, replaced); err != nil {
				return err
			}
		}
	}
	return p.retireWorkers(ctx, ref, func(name string, worker live.QueueWorker) bool {
		return worker.App == app.App && !slices.Contains(declared, name)
	})
}

func (p *Provider) recordWorker(ctx context.Context, ref provider.StackRef, name string, worker live.QueueWorker) (string, error) {
	record, err := json.Marshal(worker)
	if err != nil {
		return "", err
	}
	replaced := ""
	err = keyvalue.Change(ctx, p.keyValues, live.QueueWorkerKey(ref.Tier, ref.Project, ref.Name.Env, name), func(recorded keyvalue.Entry) ([]byte, bool, error) {
		replaced = ""
		if len(recorded.Value) > 0 {
			var previous live.QueueWorker
			if err := json.Unmarshal(recorded.Value, &previous); err == nil {
				replaced = previous.Container
			}
		}
		return record, string(recorded.Value) != string(record), nil
	})
	return replaced, err
}

func (p *Provider) retireWorkers(ctx context.Context, ref provider.StackRef, retiring func(name string, worker live.QueueWorker) bool) error {
	partition, under := live.QueueWorkersUnder(ref.Tier, ref.Project, ref.Name.Env)
	entries, err := p.keyValues.List(ctx, partition, under...)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Key.Path[len(entry.Key.Path)-1]
		var worker live.QueueWorker
		if err := json.Unmarshal(entry.Value, &worker); err != nil || !retiring(name, worker) {
			continue
		}
		if err := p.host.TakeDown(ctx, ref.Tier, worker.Container); err != nil {
			return err
		}
		if err := keyvalue.ForgetMatching(ctx, p.keyValues, entry.Key, func(recorded keyvalue.Entry) (bool, error) {
			return string(recorded.Value) == string(entry.Value), nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) removeWorkers(ctx context.Context, ref provider.StackRef) error {
	if ref.Project == "" || ref.Name.Env == "" {
		return nil
	}
	return p.retireWorkers(ctx, ref, func(_ string, worker live.QueueWorker) bool {
		return worker.Stack == ref.Name.String()
	})
}

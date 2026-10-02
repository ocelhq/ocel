package vps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func hasQueue(app *provider.AppSpec) bool {
	return len(app.Workers) > 0 || slices.ContainsFunc(app.Values.Bindings, func(binding provider.Binding) bool {
		return binding.Type == provider.BindingTopic || binding.Type == provider.BindingTask
	})
}

func newWorkerEnv(app *provider.AppSpec, worker, deliverySecret string) map[string]string {
	env := maps.Clone(app.Values.ContainerEnv)
	if env == nil {
		env = map[string]string{}
	}
	delete(env, originguard.OriginSecretPreviousVar)
	env[originguard.OriginSecretVar] = deliverySecret
	env[processenv.WorkerEnvVar] = worker
	return env
}

func (p *Provider) openDeliverySecret(ctx context.Context, ref provider.StackRef) (string, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, p.keyValues, live.QueueDatabaseKey(ref.Tier, ref.Project, ref.Name.Env))
	if err != nil {
		return "", err
	}
	var database live.QueueDatabase
	if len(recorded.Value) > 0 {
		if err := json.Unmarshal(recorded.Value, &database); err != nil {
			return "", fmt.Errorf("read %s's queue database record: %w", ref.Name.Env, err)
		}
	}
	if database.DeliverySealed == "" {
		return mintResourceSecret()
	}
	sealed, err := base64.StdEncoding.DecodeString(database.DeliverySealed)
	if err != nil {
		return "", fmt.Errorf("the delivery secret recorded for %s's queue is not base64: %w", ref.Name.Env, err)
	}
	bound, err := live.NewQueueDeliverySecretAssociatedData(ref.Project, ref.Tier, database.Stack)
	if err != nil {
		return "", err
	}
	opened, err := p.cipher.Open(ctx, ref.Tier, bound, sealed)
	if err != nil {
		return "", fmt.Errorf("open the delivery secret of %s's queue: %w", ref.Name.Env, err)
	}
	return string(opened), nil
}

func (p *Provider) runWorkers(ctx context.Context, spec provider.StackSpec, manifest []byte, progress progress.Log) error {
	app, ref := spec.App, spec.Ref
	deliverySecret := ""
	if len(app.Workers) > 0 {
		var err error
		if deliverySecret, err = p.openDeliverySecret(ctx, ref); err != nil {
			return err
		}
	}
	declared := make([]string, 0, len(app.Workers))
	for _, worker := range app.Workers {
		declared = append(declared, worker.Name)
		name := host.ContainerName(ref.Name.String(), app.App+"-"+worker.Name, app.Deployment, app.Image)
		if progress != nil {
			progress.Say("Starting worker " + worker.Name + " from " + app.App + "'s image as " + name)
		}
		if err := p.host.RunContainer(ctx, host.Container{
			Name: name, Project: ref.Project, App: app.App, Image: app.Image, Tier: ref.Tier,
			Env: newWorkerEnv(app, worker.Name, deliverySecret), Manifest: manifest, Resolved: true,
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

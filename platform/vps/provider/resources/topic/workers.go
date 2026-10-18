package topic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/component"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
)

type Workers struct{ box component.Box }

func NewWorkers(box component.Box) Workers { return Workers{box: box} }

func newEnv(app *provider.AppSpec, worker, deliverySecret string) map[string]string {
	env := maps.Clone(app.Values.ContainerEnv)
	if env == nil {
		env = map[string]string{}
	}
	delete(env, edge.OriginSecretPreviousVar)
	env[edge.OriginFrontedVar] = "1"
	env[edge.OriginSecretVar] = deliverySecret
	env[processenv.WorkerEnvVar] = worker
	return env
}

func (w Workers) openDeliverySecret(ctx context.Context, ref provider.StackRef) (string, error) {
	recorded, err := records.ReadOrEmpty(ctx, w.box.KeyValues, boxapi.QueueDatabaseKey(ref.Tier, ref.Project, ref.Name.Env))
	if err != nil {
		return "", err
	}
	var database boxapi.QueueDatabase
	if len(recorded.Value) > 0 {
		if err := json.Unmarshal(recorded.Value, &database); err != nil {
			return "", fmt.Errorf("read %s's queue database record: %w", ref.Name.Env, err)
		}
	}
	if database.DeliverySealed == "" {
		return component.MintSecret()
	}
	sealed, err := base64.StdEncoding.DecodeString(database.DeliverySealed)
	if err != nil {
		return "", fmt.Errorf("the delivery secret recorded for %s's queue is not base64: %w", ref.Name.Env, err)
	}
	bound, err := boxapi.NewQueueDeliverySecretAssociatedData(ref.Project, ref.Tier, database.Stack)
	if err != nil {
		return "", err
	}
	opened, err := w.box.Cipher.Open(ctx, ref.Tier, bound, sealed)
	if err != nil {
		return "", fmt.Errorf("open the delivery secret of %s's queue: %w", ref.Name.Env, err)
	}
	return string(opened), nil
}

func (w Workers) Run(ctx context.Context, spec provider.StackSpec, manifest []byte, progress progress.Log) error {
	app, ref := spec.App, spec.Ref
	deliverySecret := ""
	if len(app.Workers) > 0 {
		var err error
		if deliverySecret, err = w.openDeliverySecret(ctx, ref); err != nil {
			return err
		}
	}
	for _, worker := range app.Workers {
		name := release.ContainerName(ref.Name.String(), app.App+"-"+worker.Name, app.BuildID, app.Image)
		progress.Say("Starting worker " + worker.Name + " from " + app.App + "'s image as " + name)
		if _, err := w.box.Apps.Run(ctx, release.Container{
			Name: name, Project: ref.Project, App: app.App, Image: app.Image, Tier: ref.Tier,
			Env: newEnv(app, worker.Name, deliverySecret), Manifest: manifest, Resolved: true,
		}); err != nil {
			return err
		}
		replaced, err := w.record(ctx, ref, worker.Name, boxapi.QueueWorker{
			App: app.App, Stack: ref.Name.String(), Container: name, Concurrency: worker.Concurrency,
		})
		if err != nil {
			return err
		}
		if replaced != "" && replaced != name {
			if err := w.box.Apps.TakeDown(ctx, ref.Tier, replaced); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w Workers) record(ctx context.Context, ref provider.StackRef, name string, worker boxapi.QueueWorker) (string, error) {
	record, err := json.Marshal(worker)
	if err != nil {
		return "", err
	}
	replaced := ""
	err = records.Change(ctx, w.box.KeyValues, boxapi.QueueWorkerKey(ref.Tier, ref.Project, ref.Name.Env, name), func(recorded records.Entry) ([]byte, bool, error) {
		replaced = ""
		if len(recorded.Value) > 0 {
			var previous boxapi.QueueWorker
			if err := json.Unmarshal(recorded.Value, &previous); err == nil {
				replaced = previous.Container
			}
		}
		return record, string(recorded.Value) != string(record), nil
	})
	return replaced, err
}

func (w Workers) Retire(ctx context.Context, ref provider.StackRef, retiring func(name string, worker boxapi.QueueWorker) bool) error {
	partition, under := boxapi.QueueWorkersUnder(ref.Tier, ref.Project, ref.Name.Env)
	entries, err := w.box.KeyValues.List(ctx, partition, under...)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Key.Path[len(entry.Key.Path)-1]
		var worker boxapi.QueueWorker
		if err := json.Unmarshal(entry.Value, &worker); err != nil || !retiring(name, worker) {
			continue
		}
		if err := w.box.Apps.TakeDown(ctx, ref.Tier, worker.Container); err != nil {
			return err
		}
		if err := records.ForgetMatching(ctx, w.box.KeyValues, entry.Key, func(recorded records.Entry) (bool, error) {
			return string(recorded.Value) == string(entry.Value), nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (w Workers) Remove(ctx context.Context, ref provider.StackRef) error {
	if ref.Project == "" || ref.Name.Env == "" {
		return nil
	}
	return w.Retire(ctx, ref, func(_ string, worker boxapi.QueueWorker) bool {
		return worker.Stack == ref.Name.String()
	})
}

package topic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
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

func (w Workers) Run(ctx context.Context, spec provider.StackSpec, appContainer string, manifest []byte, progress progress.Log) error {
	app, ref := spec.App, spec.Ref
	deliverySecret := ""
	if len(app.Workers) > 0 {
		var err error
		if deliverySecret, err = w.openDeliverySecret(ctx, ref); err != nil {
			return err
		}
	}
	for _, worker := range app.Workers {
		name, err := release.ContainerName(ref.Project, ref.Name, worker.Name, app.Image)
		if err != nil {
			return err
		}
		progress.Say("Starting worker " + worker.Name + " from " + app.App + "'s image as " + name)
		if _, err := w.box.Apps.Run(ctx, release.Container{
			Name: name, Project: ref.Project, App: app.App, Image: app.Image, Tier: ref.Tier,
			Env: newEnv(app, worker.Name, deliverySecret), Manifest: manifest, Resolved: true,
		}); err != nil {
			return err
		}
		if _, err := w.record(ctx, boxapi.QueueReleaseWorkerKey(ref.Tier, ref.Project, ref.Name.Env, appContainer, worker.Name), boxapi.QueueWorker{
			App: app.App, Stack: ref.Name.String(), Container: name, Image: app.Image, Concurrency: worker.Concurrency,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (w Workers) Promote(ctx context.Context, tier environment.Tier, project, env, app, appContainer string, progress progress.Log) error {
	partition, under := boxapi.QueueReleaseWorkersUnder(tier, project, env, appContainer)
	released, err := w.readWorkers(ctx, partition, under)
	if err != nil {
		return err
	}
	partition, under = boxapi.QueueWorkersUnder(tier, project, env)
	serving, err := w.readWorkers(ctx, partition, under)
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(released)) {
		worker := released[name].worker
		if serving[name].worker == worker {
			continue
		}
		ran, err := w.box.Apps.Run(ctx, release.Container{Name: worker.Container, Project: project, App: worker.App, Image: worker.Image, Tier: tier})
		if err != nil {
			return err
		}
		if ran == release.RunRestarted {
			progress.Say("Started worker " + name + "'s container " + worker.Container + " again")
		}
		replaced, err := w.record(ctx, boxapi.QueueWorkerKey(tier, project, env, name), worker)
		if err != nil {
			return err
		}
		if replaced != "" && replaced != worker.Container {
			w.stop(ctx, name, replaced, progress)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(serving)) {
		recorded := serving[name]
		if _, declared := released[name]; declared || recorded.worker.App != app {
			continue
		}
		if err := w.forget(ctx, recorded.entry); err != nil {
			return err
		}
		w.stop(ctx, name, recorded.worker.Container, progress)
	}
	return nil
}

func (w Workers) stop(ctx context.Context, name, container string, progress progress.Log) {
	progress.Say("Stopping worker " + name + "'s container " + container)
	if err := w.box.Apps.Stop(ctx, container); err != nil {
		progress.Warn(fmt.Sprintf("Worker %s's container %s is still running, and no run is delivered to it: %v", name, container, err))
	}
}

type recordedWorker struct {
	entry  records.Entry
	worker boxapi.QueueWorker
}

func (w Workers) readWorkers(ctx context.Context, partition records.Partition, under []string) (map[string]recordedWorker, error) {
	entries, err := w.box.KeyValues.List(ctx, partition, under...)
	if err != nil {
		return nil, err
	}
	workers := make(map[string]recordedWorker, len(entries))
	for _, entry := range entries {
		var worker boxapi.QueueWorker
		if err := json.Unmarshal(entry.Value, &worker); err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Key, err)
		}
		workers[entry.Key.Path[len(entry.Key.Path)-1]] = recordedWorker{entry: entry, worker: worker}
	}
	return workers, nil
}

func (w Workers) record(ctx context.Context, key records.Key, worker boxapi.QueueWorker) (string, error) {
	record, err := json.Marshal(worker)
	if err != nil {
		return "", err
	}
	replaced := ""
	err = records.Change(ctx, w.box.KeyValues, key, func(recorded records.Entry) ([]byte, bool, error) {
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

func (w Workers) forget(ctx context.Context, recorded records.Entry) error {
	return records.ForgetMatching(ctx, w.box.KeyValues, recorded.Key, func(current records.Entry) (bool, error) {
		return string(current.Value) == string(recorded.Value), nil
	})
}

func (w Workers) Retire(ctx context.Context, ref provider.StackRef, retiring func(name string, worker boxapi.QueueWorker) bool) error {
	partition, under := boxapi.QueueWorkersUnder(ref.Tier, ref.Project, ref.Name.Env)
	serving, err := w.readWorkers(ctx, partition, under)
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(serving)) {
		recorded := serving[name]
		if !retiring(name, recorded.worker) {
			continue
		}
		if err := w.box.Apps.TakeDown(ctx, ref.Tier, recorded.worker.Container); err != nil {
			return err
		}
		if err := w.forget(ctx, recorded.entry); err != nil {
			return err
		}
	}
	return nil
}

func (w Workers) ListContainers(ctx context.Context, ref provider.StackRef, appContainer string) ([]string, error) {
	partition, under := boxapi.QueueReleaseWorkersUnder(ref.Tier, ref.Project, ref.Name.Env, appContainer)
	released, err := w.readWorkers(ctx, partition, under)
	if err != nil {
		return nil, err
	}
	containers := make([]string, 0, len(released))
	for _, name := range slices.Sorted(maps.Keys(released)) {
		containers = append(containers, released[name].worker.Container)
	}
	return containers, nil
}

func (w Workers) Remove(ctx context.Context, ref provider.StackRef, appContainer string) error {
	if ref.Project == "" || ref.Name.Env == "" {
		return nil
	}
	partition, under := boxapi.QueueReleaseWorkersUnder(ref.Tier, ref.Project, ref.Name.Env, appContainer)
	released, err := w.readWorkers(ctx, partition, under)
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(released)) {
		container := released[name].worker.Container
		if err := records.ForgetMatching(ctx, w.box.KeyValues, boxapi.QueueWorkerKey(ref.Tier, ref.Project, ref.Name.Env, name), func(current records.Entry) (bool, error) {
			var serving boxapi.QueueWorker
			return json.Unmarshal(current.Value, &serving) == nil && serving.Container == container, nil
		}); err != nil {
			return err
		}
		if err := w.box.Apps.TakeDown(ctx, ref.Tier, container); err != nil {
			return err
		}
		if err := w.forget(ctx, released[name].entry); err != nil {
			return err
		}
	}
	return nil
}

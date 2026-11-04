package providerserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
)

const RootDeployedSettings records.Root = "deployedsettings"

func deployedSettingsKey(tier environment.Tier, slug string) records.Key {
	return records.Partition{Tier: tier, Root: RootDeployedSettings}.Key(slug)
}

type RecordedSettings struct {
	Settings *contractv1.DeployedSettings
	Revision records.Revision
}

func ReadDeployedSettings(ctx context.Context, store records.Store, slug string, tier environment.Tier) (RecordedSettings, bool, error) {
	entry, err := store.Read(ctx, deployedSettingsKey(tier, slug))
	if errors.Is(err, records.ErrNotFound) {
		return RecordedSettings{}, false, nil
	}
	if err != nil {
		return RecordedSettings{}, false, err
	}
	settings := &contractv1.DeployedSettings{}
	if err := protojson.Unmarshal(entry.Value, settings); err != nil {
		return RecordedSettings{}, false, fmt.Errorf("read the settings %s last deployed with in %s: %w", slug, tier, err)
	}
	return RecordedSettings{Settings: settings, Revision: entry.Revision}, true, nil
}

func ReadDeployedRevision(ctx context.Context, store records.Store, slug string, tier environment.Tier) (records.Revision, bool, error) {
	entry, err := store.Read(ctx, deployedSettingsKey(tier, slug))
	if errors.Is(err, records.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return entry.Revision, true, nil
}

func deployedSettingsOf(options provider.Options, transforms []string, kind edge.Kind, selection *contractv1.EdgeSelection) (*contractv1.DeployedSettings, error) {
	served := proto.CloneOf(selection)
	if served == nil {
		served = &contractv1.EdgeSelection{}
	}
	served.Kind = string(kind)
	settings := &contractv1.DeployedSettings{Transforms: slices.Clone(transforms), Edge: served}
	if len(options) > 0 {
		encoded, err := structpb.NewStruct(options)
		if err != nil {
			return nil, fmt.Errorf("record the provider's options: %w", err)
		}
		settings.Options = encoded
	}
	return settings, nil
}

func recordDeployedSettings(ctx context.Context, store records.Store, slug string, tier environment.Tier, settings *contractv1.DeployedSettings) error {
	value, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(settings)
	if err != nil {
		return err
	}
	return records.Change(ctx, store, deployedSettingsKey(tier, slug), func(entry records.Entry) ([]byte, bool, error) {
		if len(entry.Value) > 0 {
			recorded := &contractv1.DeployedSettings{}
			if protojson.Unmarshal(entry.Value, recorded) == nil && proto.Equal(recorded, settings) {
				return nil, false, nil
			}
		}
		return value, !bytes.Equal(entry.Value, value), nil
	})
}

func forgetDeployedSettings(ctx context.Context, store records.Store, slug string, tier environment.Tier) error {
	return records.Forget(ctx, store, deployedSettingsKey(tier, slug))
}

func (r *deployRun) recordDeployedSettings(ctx context.Context) error {
	ctx, stop := newRecordContext(ctx)
	defer stop()
	settings, err := deployedSettingsOf(r.options, r.transforms, r.front.Kind(), r.selection)
	if err != nil {
		return err
	}
	return recordDeployedSettings(ctx, r.provider.KeyValues(), r.spec.Slug, r.spec.Tier, settings)
}

func ownerOfDeployedSettings(key records.Key) (records.Owner, bool) {
	if key.Partition.Root == RootDeployedSettings && len(key.Path) == 1 {
		return records.Owner{Project: key.Path[0]}, true
	}
	return records.Owner{}, false
}

package live

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	vars "github.com/ocelhq/ocel/platform/aws/provider/vars/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type storeSource struct {
	reader   envvars.EnvironmentReader
	cells    []envvars.Cell
	bindings []live.Binding

	mu       sync.Mutex
	reported map[string]int64
}

func (f *storeSource) Fetch(ctx context.Context) (map[string]string, error) {
	resolved, err := f.reader.Values(ctx, f.cells)
	if err != nil {
		return nil, err
	}
	records, err := f.reader.Bindings(ctx, bindingNames(f.bindings))
	if err != nil {
		return nil, err
	}
	for _, lag := range f.unreportedGrantLag(records) {
		fmt.Fprintln(os.Stderr, "ocel: "+lag)
	}
	return merged(resolved, f.bindings, records), nil
}

func (f *storeSource) unreportedGrantLag(records []envvars.StoredBinding) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reported == nil {
		f.reported = map[string]int64{}
	}

	var out []string
	for _, lag := range grantLag(f.bindings, records) {
		if f.reported[lag.Name] == lag.Version {
			continue
		}
		f.reported[lag.Name] = lag.Version
		out = append(out, lag.Message)
	}
	return out
}

type lagged struct {
	Name    string
	Version int64
	Message string
}

func grantLag(bindings []live.Binding, records []envvars.StoredBinding) []lagged {
	var out []lagged
	for i, record := range records {
		granted := bindings[i].Granted
		if granted == 0 || record.Version <= granted {
			continue
		}
		out = append(out, lagged{Name: record.Name, Version: record.Version, Message: fmt.Sprintf(
			"binding %s has been published %s since this deployment's IAM grants were rendered, from version %d. "+
				"Its values are live and current; its permissions are not, and ocel widens no permission on its own — deploy again to move them to version %d",
			record.Name, republished(record.Version-granted), granted, record.Version,
		)})
	}
	return out
}

func republished(n int64) string {
	if n == 1 {
		return "once more"
	}
	return fmt.Sprintf("%d more times", n)
}

func bindingNames(bindings []live.Binding) []string {
	names := make([]string, 0, len(bindings))
	for _, l := range bindings {
		names = append(names, l.Name)
	}
	return names
}

func merged(resolved map[string]string, bindings []live.Binding, records []envvars.StoredBinding) map[string]string {
	out := make(map[string]string, len(resolved)+len(records))
	maps.Copy(out, resolved)
	for i, record := range records {
		out[bindings[i].Key] = string(record.Value)
	}
	return out
}

func Resolve(ctx context.Context, taskRoot string) (*live.Values, error) {
	raw, err := os.ReadFile(filepath.Join(taskRoot, vars.FilePath))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", vars.FilePath, err)
	}
	return FromManifest(ctx, raw)
}

func FromManifest(ctx context.Context, raw []byte) (*live.Values, error) {
	manifest, err := vars.Parse(raw)
	if err != nil {
		return nil, err
	}
	if !manifest.Live() {
		return nil, nil
	}

	cfg, err := sdkconfig.Workload(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return live.New(&storeSource{
		reader: envvars.EnvironmentReader{
			Records:     awsports.Records{Dynamo: dynamodb.NewFromConfig(cfg), Tables: awsports.Table(manifest.Table)},
			Cipher:      awsports.Cipher{KMS: kms.NewFromConfig(cfg), Keys: awsports.Key(manifest.KeyARN)},
			Scope:       envvars.Scope{Project: manifest.Slug, Class: edge.Class(manifest.Class)},
			Environment: manifest.Environment,
		},
		cells:    manifestCells(manifest),
		bindings: manifest.Bindings,
	}, live.Keys(manifest.Keys, manifest.Bindings), manifest.Bindings, nil), nil
}

func manifestCells(m vars.Manifest) []envvars.Cell {
	cells := make([]envvars.Cell, 0, len(m.Keys))
	for _, k := range m.Keys {
		cells = append(cells, envvars.Cell{Folder: k.Folder, Key: k.Key})
	}
	return cells
}

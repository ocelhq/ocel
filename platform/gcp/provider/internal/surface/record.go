package surface

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
)

const RootPatches records.Root = "patches"

type Recorded struct {
	Paths  []string `json:"paths,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

func (r Recorded) IsEmpty() bool { return len(r.Paths) == 0 && len(r.Labels) == 0 }

func (r Recorded) Equal(other Recorded) bool {
	return slices.Equal(r.Paths, other.Paths) && slices.Equal(r.Labels, other.Labels)
}

func (r Recorded) With(other Recorded) Recorded {
	return Recorded{Paths: union(r.Paths, other.Paths), Labels: union(r.Labels, other.Labels)}
}

func union(a, b []string) []string {
	joined := slices.Concat(a, b)
	slices.Sort(joined)
	return slices.Compact(joined)
}

type Record struct {
	Store records.Store
	Key   records.Key
}

func KeyFor(ref provider.StackRef, kind, name string, under ...string) records.Key {
	return records.Partition{Tier: ref.Tier, Root: RootPatches}.Key(append([]string{ref.Project, ref.Name.Env, kind, name}, under...)...)
}

func (r Record) Read(ctx context.Context) (Recorded, error) {
	entry, err := records.ReadOrEmpty(ctx, r.Store, r.Key)
	if err != nil {
		return Recorded{}, fmt.Errorf("read what a transform patched under %s: %w", r.Key, err)
	}
	return decodeRecorded(entry)
}

func decodeRecorded(entry records.Entry) (Recorded, error) {
	var recorded Recorded
	if len(entry.Value) == 0 {
		return recorded, nil
	}
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return Recorded{}, fmt.Errorf("read what a transform patched under %s: %w", entry.Key, err)
	}
	return recorded, nil
}

func (r Record) Write(ctx context.Context, applied Recorded) error {
	if applied.IsEmpty() {
		return r.Forget(ctx)
	}
	value, err := json.Marshal(applied)
	if err != nil {
		return err
	}
	return records.Change(ctx, r.Store, r.Key, func(entry records.Entry) ([]byte, bool, error) {
		current, err := decodeRecorded(entry)
		if err != nil {
			return nil, false, err
		}
		return value, !current.Equal(applied), nil
	})
}

func (r Record) Apply(ctx context.Context, before, applied Recorded, apply func() error) error {
	if reaching := before.With(applied); !reaching.Equal(before) {
		if err := r.Write(ctx, reaching); err != nil {
			return err
		}
	}
	if err := apply(); err != nil {
		return err
	}
	if applied.Equal(before) {
		return nil
	}
	return r.Write(ctx, applied)
}

func (r Record) Forget(ctx context.Context) error {
	return records.Forget(ctx, r.Store, r.Key)
}

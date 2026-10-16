package stackrecords

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
)

type BootstrapWant struct {
	WantedAt time.Time `json:"wanted_at"`
}

func RecordBootstrapWant(ctx context.Context, store keyvalue.Store, tier environment.Tier, part, project string) error {
	value, err := json.Marshal(BootstrapWant{WantedAt: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("record that %s wants the %s bootstrap part %s: %w", project, tier, part, err)
	}
	_, err = store.Write(ctx, keyvalue.Entry{Key: BootstrapWantKey(tier, project, part), Value: value})
	if err != nil && !errors.Is(err, keyvalue.ErrStale) {
		return fmt.Errorf("record that %s wants the %s bootstrap part %s: %w", project, tier, part, err)
	}
	return nil
}

func RecordWantedPart(ctx context.Context, store keyvalue.Store, tier environment.Tier, project string, refused error) error {
	var wanted provider.WantedPartError
	if project == "" || !errors.As(refused, &wanted) {
		return refused
	}
	if err := RecordBootstrapWant(ctx, store, tier, wanted.Part, project); err != nil {
		return errors.Join(refused, err)
	}
	return refused
}

func ListBootstrapWants(ctx context.Context, store keyvalue.Store, tier environment.Tier) (map[string][]string, error) {
	recorded, err := listWants(ctx, store, tier)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(recorded, func(a, b recordedWant) int {
		return cmp.Or(a.at.Compare(b.at), strings.Compare(a.project, b.project))
	})
	wants := map[string][]string{}
	for _, want := range recorded {
		wants[want.part] = append(wants[want.part], want.project)
	}
	return wants, nil
}

func ForgetBootstrapWants(ctx context.Context, store keyvalue.Store, tier environment.Tier, part string) error {
	return forgetWants(ctx, store, tier, func(want recordedWant) bool { return want.part == part })
}

func ForgetProjectWants(ctx context.Context, store keyvalue.Store, tier environment.Tier, project string) error {
	return forgetWants(ctx, store, tier, func(recordedWant) bool { return true }, project)
}

type recordedWant struct {
	key     keyvalue.Key
	project string
	part    string
	at      time.Time
}

func listWants(ctx context.Context, store keyvalue.Store, tier environment.Tier, project ...string) ([]recordedWant, error) {
	entries, err := store.List(ctx, bootstrapPartition(tier), append([]string{wantsSegment}, project...)...)
	if err != nil {
		return nil, fmt.Errorf("read the projects that want a %s bootstrap part: %w", tier, err)
	}
	var wants []recordedWant
	for _, entry := range entries {
		rest, ok := entry.Key.Under(wantsSegment)
		if !ok || len(rest) != 2 || len(entry.Value) == 0 {
			continue
		}
		var want BootstrapWant
		if err := json.Unmarshal(entry.Value, &want); err != nil {
			return nil, fmt.Errorf("read the bootstrap want %s: %w", entry.Key, err)
		}
		wants = append(wants, recordedWant{key: entry.Key, project: rest[0], part: rest[1], at: want.WantedAt})
	}
	return wants, nil
}

func forgetWants(ctx context.Context, store keyvalue.Store, tier environment.Tier, match func(recordedWant) bool, project ...string) error {
	wants, err := listWants(ctx, store, tier, project...)
	if err != nil {
		return err
	}
	for _, want := range wants {
		if !match(want) {
			continue
		}
		if err := keyvalue.Forget(ctx, store, want.key); err != nil {
			return fmt.Errorf("forget that %s wants the %s bootstrap part %s: %w", want.project, tier, want.part, err)
		}
	}
	return nil
}

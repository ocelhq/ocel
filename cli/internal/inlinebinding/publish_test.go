package inlinebinding

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

type heldRecord struct {
	owner   string
	version uint64
}

type recordStore struct {
	mu      sync.Mutex
	records map[string]heldRecord
}

func storeHolding(names ...string) *recordStore {
	s := &recordStore{records: map[string]heldRecord{}}
	for _, name := range names {
		s.records[name] = heldRecord{owner: naming.InlineRecordOwner, version: 1}
	}
	return s
}

func (s *recordStore) SetBinding(_ context.Context, req *envvarsv1.SetBindingRequest) (*envvarsv1.SetBindingResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := req.GetBinding().GetName()
	held := s.records[name]
	held.owner, held.version = req.GetOwner(), held.version+1
	s.records[name] = held
	return &envvarsv1.SetBindingResponse{Version: held.version}, nil
}

func (s *recordStore) RemoveBinding(_ context.Context, req *envvarsv1.RemoveBindingRequest) (*envvarsv1.RemoveBindingResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, held := s.records[req.GetName()]
	delete(s.records, req.GetName())
	return &envvarsv1.RemoveBindingResponse{Removed: held}, nil
}

func (s *recordStore) ListBindings(context.Context, *envvarsv1.ListBindingsRequest) (*envvarsv1.ListBindingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &envvarsv1.ListBindingsResponse{}
	for name, held := range s.records {
		resp.Bindings = append(resp.Bindings, &envvarsv1.BindingSummary{Name: name, Owner: held.owner, Version: held.version})
	}
	return resp, nil
}

func (s *recordStore) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for name := range s.records {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func kept(name string) Record {
	return Record{Site: "bindings." + name, Binding: &bindingsv1.Binding{Name: name}}
}

var production = Coordinate{Slug: "shop", Tier: environmentv1.Tier_TIER_PRODUCTION}

func publishing(s *recordStore, name string) {
	_, _ = s.SetBinding(context.Background(), &envvarsv1.SetBindingRequest{
		Binding: &bindingsv1.Binding{Name: name},
		Owner:   naming.InlineRecordOwner,
	})
}

func TestDeploy(t *testing.T) {
	t.Run("a record no binding keeps any more is removed once the deploy succeeds", func(t *testing.T) {
		store := storeHolding("ocel:postgres.orders", "ocel:bucket.uploads")
		err := Deploy(context.Background(), store, production, []Record{kept("ocel:postgres.orders")}, func() error { return nil })
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		if got := store.names(); !slices.Equal(got, []string{"ocel:postgres.orders"}) {
			t.Errorf("records = %v, want only the one a binding keeps", got)
		}
	})

	t.Run("a failed deploy removes nothing", func(t *testing.T) {
		store := storeHolding("ocel:postgres.orders", "ocel:bucket.uploads")
		failed := errors.New("the deploy failed")
		err := Deploy(context.Background(), store, production, []Record{kept("ocel:postgres.orders")}, func() error { return failed })
		if !errors.Is(err, failed) {
			t.Fatalf("Deploy = %v, want the deploy's failure", err)
		}
		if got := store.names(); len(got) != 2 {
			t.Errorf("records = %v, want both kept for the release still serving", got)
		}
	})

	t.Run("a record another deploy published while this one ran is left to that deploy", func(t *testing.T) {
		store := storeHolding("ocel:postgres.orders", "ocel:bucket.uploads")
		err := Deploy(context.Background(), store, production, []Record{kept("ocel:postgres.orders")}, func() error {
			publishing(store, "ocel:bucket.uploads")
			publishing(store, "ocel:bucket.avatars")
			return nil
		})
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		if got := store.names(); !slices.Equal(got, []string{"ocel:bucket.avatars", "ocel:bucket.uploads", "ocel:postgres.orders"}) {
			t.Errorf("records = %v, want the ones the other deploy wrote kept", got)
		}
	})

	t.Run("a record some other publisher owns is never removed", func(t *testing.T) {
		store := storeHolding()
		store.records["warehouse"] = heldRecord{owner: "infra", version: 1}
		if err := Deploy(context.Background(), store, production, nil, func() error { return nil }); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		if got := store.names(); !slices.Equal(got, []string{"warehouse"}) {
			t.Errorf("records = %v, want the published record left alone", got)
		}
	})
}

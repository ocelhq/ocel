package live

import (
	"context"
	"errors"
	"slices"
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

type fakeReader struct {
	values      map[string]string
	records     []variablestore.StoredBinding
	valuesErr   error
	bindingsErr error
	cells       []variablestore.Cell
	names       []string
}

func (r *fakeReader) Values(_ context.Context, cells []variablestore.Cell) (map[string]string, error) {
	r.cells = cells
	return r.values, r.valuesErr
}

func (r *fakeReader) Bindings(_ context.Context, names []string) ([]variablestore.StoredBinding, error) {
	r.names = names
	return r.records, r.bindingsErr
}

func publishedPostgres(name string) []byte {
	return []byte(`{"binding":{"name":"` + name + `","postgres":{"host":"h","port":5432,"database":"d","username":"u","password":"p"}}}`)
}

func TestFetch(t *testing.T) {
	t.Parallel()

	t.Run("reads the cells and every binding's name through the reader", func(t *testing.T) {
		t.Parallel()

		binding := postgresBinding()
		cells := []variablestore.Cell{{Key: "KEY"}}
		reader := &fakeReader{
			values:  map[string]string{"KEY": "value"},
			records: []variablestore.StoredBinding{{Name: binding.Name, Value: publishedPostgres(binding.Name)}},
		}
		values, records, err := Fetch(context.Background(), reader, cells, []Binding{binding})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if !slices.Equal(reader.cells, cells) || !slices.Equal(reader.names, []string{binding.Name}) {
			t.Errorf("read cells %v and names %v, want %v and [%s]", reader.cells, reader.names, cells, binding.Name)
		}
		if values["KEY"] != "value" || len(records) != 1 {
			t.Errorf("values = %v, records = %v, want the value and the one record", values, records)
		}
		if err := Conform([]Binding{binding}, values); err != nil {
			t.Errorf("Conform = %v, want the binding delivered under its key", err)
		}
	})

	t.Run("pairs each binding with the record published under its name, whatever order or count the reader returns", func(t *testing.T) {
		t.Parallel()

		db := postgresBinding()
		other := Binding{Name: "db--other", Key: "OCEL_RESOURCE_POSTGRES_other", Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES}
		reader := &fakeReader{records: []variablestore.StoredBinding{
			{Name: "db--stray", Value: publishedPostgres("db--stray")},
			{Name: other.Name, Value: publishedPostgres(other.Name), Version: 2},
			{Name: db.Name, Value: publishedPostgres(db.Name), Version: 1},
		}}
		values, records, err := Fetch(context.Background(), reader, nil, []Binding{db, other})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if err := Conform([]Binding{db, other}, values); err != nil {
			t.Errorf("Conform = %v, want each binding delivered", err)
		}
		if len(values) != 2 {
			t.Errorf("values = %v, want no key for a record no binding names", values)
		}
		if len(records) != 2 || records[0].Name != db.Name || records[1].Name != other.Name {
			t.Errorf("records = %v, want one per binding in the order the bindings are declared", records)
		}
	})

	t.Run("delivers one record under each key that binds its name", func(t *testing.T) {
		t.Parallel()

		db := postgresBinding()
		alias := Binding{Name: db.Name, Key: "OCEL_RESOURCE_POSTGRES_alias", Type: db.Type}
		reader := &fakeReader{records: []variablestore.StoredBinding{{Name: db.Name, Value: publishedPostgres(db.Name)}}}
		values, _, err := Fetch(context.Background(), reader, nil, []Binding{db, alias})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if err := Conform([]Binding{db, alias}, values); err != nil {
			t.Errorf("Conform = %v, want the record under both keys", err)
		}
	})

	t.Run("leaves a binding with no record to Conform rather than a resolved value of the same key", func(t *testing.T) {
		t.Parallel()

		binding := postgresBinding()
		reader := &fakeReader{values: map[string]string{binding.Key: postgresRecord}}
		values, _, err := Fetch(context.Background(), reader, nil, []Binding{binding})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if err := Conform([]Binding{binding}, values); !errors.Is(err, ErrDrift) {
			t.Errorf("Conform = %v, want drift: a value the user named like the binding must not stand in for its record", err)
		}
	})

	t.Run("returns the reader's errors as they are", func(t *testing.T) {
		t.Parallel()

		failed := errors.New("unreachable")
		for _, reader := range []*fakeReader{{valuesErr: failed}, {bindingsErr: failed}} {
			if _, _, err := Fetch(context.Background(), reader, nil, []Binding{postgresBinding()}); !errors.Is(err, failed) {
				t.Errorf("Fetch = %v, want %v", err, failed)
			}
		}
	})
}

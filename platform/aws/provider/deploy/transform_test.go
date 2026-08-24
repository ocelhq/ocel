package deploy

import (
	"context"
	"encoding/json"

	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

type fakeEvaluator struct {
	seen transform.Request
	out  []transform.Surfaces
	tags map[string]string
	err  error
}

func (f *fakeEvaluator) Evaluate(_ context.Context, req transform.Request) ([]transform.Result, error) {
	f.seen = req
	if f.err != nil {
		return nil, f.err
	}
	out := f.out
	if out == nil {
		out = make([]transform.Surfaces, len(req.Resources))
		for i, r := range req.Resources {
			out[i] = r.Surfaces
		}
	}
	results := make([]transform.Result, len(out))
	for i, surfaces := range overTheWire(out) {
		results[i] = transform.Result{Surfaces: surfaces, Tags: f.tags}
	}
	return results, nil
}

func overTheWire(surfaces []transform.Surfaces) []transform.Surfaces {
	encoded, err := json.Marshal(surfaces)
	if err != nil {
		panic(err)
	}
	var decoded []transform.Surfaces
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		panic(err)
	}
	return decoded
}

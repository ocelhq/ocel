package deploy

import (
	"context"
	"encoding/json"

	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

type fakeEvaluator struct {
	seen transform.Request
	out  []transform.Patches
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
		out = make([]transform.Patches, len(req.Resources))
		for i := range req.Resources {
			out[i] = transform.Patches{}
		}
	}
	results := make([]transform.Result, len(out))
	for i, patches := range overTheWire(out) {
		results[i] = transform.Result{Patches: patches, Tags: f.tags}
	}
	return results, nil
}

func overTheWire(patches []transform.Patches) []transform.Patches {
	encoded, err := json.Marshal(patches)
	if err != nil {
		panic(err)
	}
	var decoded []transform.Patches
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		panic(err)
	}
	return decoded
}

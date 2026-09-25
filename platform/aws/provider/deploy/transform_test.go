package deploy

import (
	"context"
	"encoding/json"

	"github.com/ocelhq/ocel/pkg/transformkit"
)

type fakePass struct {
	seen transformkit.Request
	out  []transformkit.Patches
	tags map[string]string
	err  error
}

func (f *fakePass) Evaluate(_ context.Context, req transformkit.Request) ([]transformkit.Result, error) {
	f.seen = req
	if f.err != nil {
		return nil, f.err
	}
	out := f.out
	if out == nil {
		out = make([]transformkit.Patches, len(req.Resources))
		for i := range req.Resources {
			out[i] = transformkit.Patches{}
		}
	}
	results := make([]transformkit.Result, len(out))
	for i, patches := range overTheWire(out) {
		results[i] = transformkit.Result{Patches: patches, Tags: f.tags}
	}
	return results, nil
}

func overTheWire(patches []transformkit.Patches) []transformkit.Patches {
	encoded, err := json.Marshal(patches)
	if err != nil {
		panic(err)
	}
	var decoded []transformkit.Patches
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		panic(err)
	}
	return decoded
}

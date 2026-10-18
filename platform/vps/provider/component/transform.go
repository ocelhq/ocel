package component

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
)

//go:generate go generate -C ../../../../pkg/provider/transform ./...

const (
	transformProvider = "vps"

	surfaceContainer = "container"
	surfaceVolume    = "volume"

	ownLabelPrefix = "ocel."
)

var pinnedImage = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

type Shape struct {
	Kind            string
	OwnEnv          []string
	RefuseOwnedArgs func(args []string) error
}

type containerPatch struct {
	Image   string            `json:"image"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Memory  string            `json:"memory"`
	CPUs    string            `json:"cpus"`
	ShmSize string            `json:"shmSize"`
}

type volumePatch struct {
	Driver     string            `json:"driver"`
	DriverOpts map[string]string `json:"driverOpts"`
}

func decodePatch(patch map[string]any, into any) error {
	rendered, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	strict := json.NewDecoder(bytes.NewReader(rendered))
	strict.DisallowUnknownFields()
	return strict.Decode(into)
}

func unrenderable(kind, resource, surface string, why error) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform patch to %s.%s.%s on %s is invalid: %v",
		transformProvider, kind, surface, resource, why)
}

func (b Box) Reshaped(ctx context.Context, in resources.ProvisionRequest, shape Shape, spec container.Spec) (container.Spec, error) {
	kind := shape.Kind
	if b.Transform == nil {
		return spec, nil
	}
	results, err := b.Transform.Evaluate(ctx, transform.Request{
		Provider: transformProvider,
		EnvTier:  string(in.Ref.Tier),
		Env:      in.Ref.Name.Env,
		Resources: []transform.Resource{
			{Type: kind, Name: in.Resource.Name},
		},
	})
	if err != nil || len(results) == 0 {
		return spec, err
	}
	for _, surface := range slices.Sorted(maps.Keys(results[0].Patches)) {
		patch := results[0].Patches[surface]
		switch surface {
		case surfaceContainer:
			if spec, err = containerPatched(shape, in.Resource.Name, spec, patch); err != nil {
				return spec, err
			}
		case surfaceVolume:
			var decoded volumePatch
			if err := decodePatch(patch, &decoded); err != nil {
				return spec, unrenderable(kind, in.Resource.Name, surface, err)
			}
			if decoded.Driver != "" {
				spec.Volume.Driver = decoded.Driver
			}
			if len(decoded.DriverOpts) > 0 {
				spec.Volume.Options = decoded.DriverOpts
			}
		default:
			return spec, refusal.Refuse(refusal.CodeInvalid,
				"a transform patches %s.%s.%s on %s; a box only patches %s and %s for a %s",
				transformProvider, kind, surface, in.Resource.Name, surfaceContainer, surfaceVolume, kind)
		}
	}
	for key, value := range results[0].Tags {
		if strings.HasPrefix(key, ownLabelPrefix) {
			return spec, refusal.Refuse(refusal.CodeInvalid,
				"a transform tags %s with %s; the %s prefix is reserved: rename the tag",
				in.Resource.Name, key, ownLabelPrefix)
		}
		if spec.Labels == nil {
			spec.Labels = map[string]string{}
		}
		spec.Labels[key] = value
	}
	return spec, nil
}

func containerPatched(shape Shape, resource string, spec container.Spec, patch map[string]any) (container.Spec, error) {
	kind := shape.Kind
	var decoded containerPatch
	if err := decodePatch(patch, &decoded); err != nil {
		return spec, unrenderable(kind, resource, surfaceContainer, err)
	}
	if decoded.Image != "" {
		if !pinnedImage.MatchString(decoded.Image) {
			return spec, refusal.Refuse(refusal.CodeInvalid,
				"a transform runs %s %s as unpinned image %q: pin it as <image>@sha256:<digest>",
				kind, resource, decoded.Image)
		}
		spec.Image = decoded.Image
	}
	for _, name := range slices.Sorted(maps.Keys(decoded.Env)) {
		if slices.Contains(shape.OwnEnv, name) {
			return spec, refusal.Refuse(refusal.CodeInvalid,
				"a transform sets %s %s's %s, which ocel owns: drop it",
				kind, resource, name)
		}
		spec.Env[name] = decoded.Env[name]
	}
	switch refuseOwned := shape.RefuseOwnedArgs; {
	case len(decoded.Args) == 0:
	case refuseOwned != nil:
		if err := refuseOwned(decoded.Args); err != nil {
			return spec, refusal.Refuse(refusal.CodeInvalid, "a transform sets %s %s's args: %v", kind, resource, err)
		}
		spec.Args = append(slices.Clone(spec.Args), decoded.Args...)
	default:
		spec.Args = decoded.Args
	}
	for _, limit := range []struct {
		patched string
		into    *string
	}{{decoded.Memory, &spec.Memory}, {decoded.CPUs, &spec.CPUs}, {decoded.ShmSize, &spec.ShmSize}} {
		if limit.patched != "" {
			*limit.into = limit.patched
		}
	}
	return spec, nil
}

package vps

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/pkg/transformkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

//go:generate go generate -C ../../../pkg/transformkit ./...

const (
	transformProvider     = "vps"
	transformTypePostgres = "postgres"
	transformTypeBucket   = "bucket"

	surfaceContainer = "container"
	surfaceVolume    = "volume"

	ownLabelPrefix = "ocel."
)

var pinnedImage = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

var ownEnv = map[string][]string{
	transformTypePostgres: postgresOwnEnv,
	transformTypeBucket:   storeOwnEnv,
}

var storeOwnEnv = []string{
	"RUSTFS_ACCESS_KEY", "RUSTFS_ACCESS_KEY_FILE",
	"RUSTFS_ADDRESS",
	"RUSTFS_REGION",
	"RUSTFS_ROOT_PASSWORD", "RUSTFS_ROOT_USER",
	"RUSTFS_SECRET_KEY", "RUSTFS_SECRET_KEY_FILE",
	"RUSTFS_VOLUMES",
}

var postgresOwnEnv = []string{
	"PGDATA",
	"POSTGRES_DB", "POSTGRES_DB_FILE",
	"POSTGRES_HOST_AUTH_METHOD",
	"POSTGRES_PASSWORD", "POSTGRES_PASSWORD_FILE",
	"POSTGRES_USER", "POSTGRES_USER_FILE",
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

func nodePass(modules []string) transformkit.Pass {
	root, err := os.Getwd()
	if err != nil {
		root = "."
	}
	return transformkit.NodePass{
		Root: root, Modules: modules,
		Uninstalled: "Install `@ocel/transforms` as a devDependency",
	}
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

func (p *Provider) reshaped(ctx context.Context, in resources.ProvisionRequest, kind string, spec host.ResourceContainer) (host.ResourceContainer, error) {
	if p.transform == nil {
		return spec, nil
	}
	results, err := p.transform.Evaluate(ctx, transformkit.Request{
		Provider: transformProvider,
		EnvClass: string(in.Ref.Class),
		Env:      in.Ref.Name.Env,
		Resources: []transformkit.Resource{
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
			if spec, err = containerPatched(kind, in.Resource.Name, spec, patch); err != nil {
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

func containerPatched(kind, resource string, spec host.ResourceContainer, patch map[string]any) (host.ResourceContainer, error) {
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
		if slices.Contains(ownEnv[kind], name) {
			return spec, refusal.Refuse(refusal.CodeInvalid,
				"a transform sets %s %s's %s, which ocel owns: drop it",
				kind, resource, name)
		}
		spec.Env[name] = decoded.Env[name]
	}
	if len(decoded.Args) > 0 {
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

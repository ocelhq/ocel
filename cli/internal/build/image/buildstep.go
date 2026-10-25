package image

import (
	"context"
	"maps"
	"path"
	"slices"

	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

const railpackSecretsHashMount = "/secrets-hash"

type buildStepValues struct {
	keys    []string
	envKeys []string
	env     map[string]string
}

func (v buildStepValues) isEmpty() bool { return len(v.keys)+len(v.envKeys)+len(v.env) == 0 }

type buildStepGateway struct {
	client.Client
	values buildStepValues
}

func (g buildStepGateway) Solve(ctx context.Context, req client.SolveRequest) (*client.Result, error) {
	if req.Definition != nil && !g.values.isEmpty() {
		rewritten, err := giveBuildStepValues(req.Definition, g.values)
		if err != nil {
			return nil, err
		}
		req.Definition = rewritten
	}
	return g.Client.Solve(ctx, req)
}

func isBuildStepCommand(operation *pb.Op) bool {
	exec := operation.GetExec()
	if exec == nil {
		return false
	}
	return slices.ContainsFunc(exec.Mounts, func(mount *pb.Mount) bool { return mount.Dest == railpackSecretsHashMount })
}

func hasLiveSecretEnv(exec *pb.ExecOp, keys []string) bool {
	return slices.ContainsFunc(exec.Secretenv, func(env *pb.SecretEnv) bool { return slices.Contains(keys, env.ID) })
}

func addBuildStepValues(exec *pb.ExecOp, values buildStepValues) {
	for _, key := range slices.Sorted(maps.Keys(values.env)) {
		exec.Meta.Env = append(exec.Meta.Env, key+"="+values.env[key])
	}
	if len(values.keys)+len(values.envKeys) == 0 {
		return
	}
	exec.Network = pb.NetMode_HOST
	if len(values.keys) > 0 {
		exec.Meta.Env = append(exec.Meta.Env, liveDirEnv+"="+buildLiveDir)
	}
	for _, key := range values.keys {
		exec.Mounts = append(exec.Mounts, &pb.Mount{
			Input:     int64(pb.Empty),
			Dest:      path.Join(buildLiveDir, key),
			MountType: pb.MountType_SECRET,
			SecretOpt: &pb.SecretOpt{ID: key, Mode: secretFileMode},
		})
	}
	for _, key := range values.envKeys {
		exec.Secretenv = append(exec.Secretenv, &pb.SecretEnv{ID: key, Name: key})
	}
}

func giveBuildStepValues(definition *pb.Definition, values buildStepValues) (*pb.Definition, error) {
	secrets := slices.Concat(values.keys, values.envKeys)
	renamed := map[string]string{}
	rewritten := &pb.Definition{
		Def:      make([][]byte, len(definition.Def)),
		Metadata: make(map[string]*pb.OpMetadata, len(definition.Metadata)),
		Source:   definition.Source,
	}
	changed := false
	for i, raw := range definition.Def {
		var operation pb.Op
		if err := operation.UnmarshalVT(raw); err != nil {
			return nil, err
		}
		touched := false
		for _, input := range operation.Inputs {
			if next, ok := renamed[input.Digest]; ok {
				input.Digest = next
				touched = true
			}
		}
		if exec := operation.GetExec(); exec != nil && hasLiveSecretEnv(exec, secrets) {
			exec.Secretenv = slices.DeleteFunc(exec.Secretenv, func(env *pb.SecretEnv) bool { return slices.Contains(secrets, env.ID) })
			touched = true
		}
		if isBuildStepCommand(&operation) {
			addBuildStepValues(operation.GetExec(), values)
			touched = true
		}
		if touched {
			remarshaled, err := operation.MarshalVT()
			if err != nil {
				return nil, err
			}
			renamed[digest.FromBytes(raw).String()] = digest.FromBytes(remarshaled).String()
			raw = remarshaled
			changed = true
		}
		rewritten.Def[i] = raw
	}
	if !changed {
		return definition, nil
	}
	for old, metadata := range definition.Metadata {
		if next, ok := renamed[old]; ok {
			old = next
		}
		rewritten.Metadata[old] = metadata
	}
	if definition.Source != nil {
		source := &pb.Source{Infos: definition.Source.Infos, Locations: map[string]*pb.Locations{}}
		for old, locations := range definition.Source.Locations {
			if next, ok := renamed[old]; ok {
				old = next
			}
			source.Locations[old] = locations
		}
		rewritten.Source = source
	}
	return rewritten, nil
}

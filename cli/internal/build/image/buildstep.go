package image

import (
	"context"
	"path"
	"slices"

	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

const railpackSecretsHashMount = "/secrets-hash"

type liveGateway struct {
	client.Client
	keys []string
}

func (g liveGateway) Solve(ctx context.Context, req client.SolveRequest) (*client.Result, error) {
	if req.Definition != nil && len(g.keys) > 0 {
		rewritten, err := mountLiveValues(req.Definition, g.keys)
		if err != nil {
			return nil, err
		}
		req.Definition = rewritten
	}
	return g.Client.Solve(ctx, req)
}

func isBuildStepCommand(op *pb.Op) bool {
	exec := op.GetExec()
	if exec == nil {
		return false
	}
	return slices.ContainsFunc(exec.Mounts, func(m *pb.Mount) bool { return m.Dest == railpackSecretsHashMount })
}

func hasLiveSecretEnv(exec *pb.ExecOp, keys []string) bool {
	return slices.ContainsFunc(exec.Secretenv, func(env *pb.SecretEnv) bool { return slices.Contains(keys, env.ID) })
}

func giveLiveValues(exec *pb.ExecOp, keys []string) {
	exec.Network = pb.NetMode_HOST
	exec.Meta.Env = append(exec.Meta.Env, liveDirEnv+"="+buildLiveDir)
	for _, key := range keys {
		exec.Mounts = append(exec.Mounts, &pb.Mount{
			Input:     int64(pb.Empty),
			Dest:      path.Join(buildLiveDir, key),
			MountType: pb.MountType_SECRET,
			SecretOpt: &pb.SecretOpt{ID: key, Mode: secretFileMode},
		})
	}
}

func mountLiveValues(def *pb.Definition, keys []string) (*pb.Definition, error) {
	renamed := map[string]string{}
	rewritten := &pb.Definition{
		Def:      make([][]byte, len(def.Def)),
		Metadata: make(map[string]*pb.OpMetadata, len(def.Metadata)),
		Source:   def.Source,
	}
	changed := false
	for i, raw := range def.Def {
		var op pb.Op
		if err := op.UnmarshalVT(raw); err != nil {
			return nil, err
		}
		touched := false
		for _, input := range op.Inputs {
			if next, ok := renamed[input.Digest]; ok {
				input.Digest = next
				touched = true
			}
		}
		if exec := op.GetExec(); exec != nil && hasLiveSecretEnv(exec, keys) {
			exec.Secretenv = slices.DeleteFunc(exec.Secretenv, func(env *pb.SecretEnv) bool { return slices.Contains(keys, env.ID) })
			touched = true
		}
		if isBuildStepCommand(&op) {
			giveLiveValues(op.GetExec(), keys)
			touched = true
		}
		if touched {
			remarshaled, err := op.MarshalVT()
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
		return def, nil
	}
	for old, metadata := range def.Metadata {
		if next, ok := renamed[old]; ok {
			old = next
		}
		rewritten.Metadata[old] = metadata
	}
	if def.Source != nil {
		source := &pb.Source{Infos: def.Source.Infos, Locations: map[string]*pb.Locations{}}
		for old, locations := range def.Source.Locations {
			if next, ok := renamed[old]; ok {
				old = next
			}
			source.Locations[old] = locations
		}
		rewritten.Source = source
	}
	return rewritten, nil
}

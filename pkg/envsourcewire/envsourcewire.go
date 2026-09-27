package envsourcewire

import (
	"cmp"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func Encode(descriptor envsource.Descriptor, read map[envvars.Cell]envsource.Value) *envvarsv1.EnvSource {
	switch {
	case descriptor.Kind == envsource.Infisical && descriptor.Infisical != nil:
		options := descriptor.Infisical
		return &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Infisical{Infisical: &envvarsv1.InfisicalEnvSource{
			Project:      options.Project,
			Environment:  options.Environment,
			Path:         options.Path,
			Host:         options.Host,
			Auth:         encodeAuth(options.Auth),
			WriteMissing: options.Write == envsource.WriteMissing,
		}}}
	case descriptor.Kind == envsource.Exec && descriptor.Exec != nil:
		sent := &envvarsv1.ExecEnvSource{Command: descriptor.Exec.Command}
		for _, at := range slices.SortedFunc(maps.Keys(read), compareCells) {
			sent.Values = append(sent.Values, &envvarsv1.EnvSourceValue{
				Cell:    &envvarsv1.Cell{Folder: at.Folder, Key: at.Key},
				Value:   string(read[at].Plaintext),
				Version: read[at].Version,
			})
		}
		return &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Exec{Exec: sent}}
	}
	return &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Builtin{Builtin: &envvarsv1.BuiltinEnvSource{}}}
}

func encodeAuth(auth envsource.InfisicalAuth) *envvarsv1.InfisicalAuth {
	switch auth.Method {
	case envsource.AuthIdentity:
		return &envvarsv1.InfisicalAuth{Method: &envvarsv1.InfisicalAuth_Identity{Identity: &envvarsv1.InfisicalIdentityAuth{IdentityId: auth.IdentityID}}}
	case envsource.AuthUniversal:
		return &envvarsv1.InfisicalAuth{Method: &envvarsv1.InfisicalAuth_Universal{Universal: &envvarsv1.InfisicalUniversalAuth{
			ClientIdVariable:     auth.ClientIDVariable,
			ClientSecretVariable: auth.ClientSecretVariable,
		}}}
	}
	return nil
}

func Decode(wire *envvarsv1.EnvSource) (envsource.Descriptor, map[envvars.Cell]envsource.Value) {
	switch {
	case wire.GetInfisical() != nil:
		sent := wire.GetInfisical()
		write := envsource.WriteNever
		if sent.GetWriteMissing() {
			write = envsource.WriteMissing
		}
		options := envsource.InfisicalOptions{
			Project:     sent.GetProject(),
			Environment: sent.GetEnvironment(),
			Path:        sent.GetPath(),
			Host:        sent.GetHost(),
			Auth:        decodeAuth(sent.GetAuth()),
			Write:       write,
		}.Normalize()
		return envsource.Descriptor{Kind: envsource.Infisical, Infisical: &options}, nil
	case wire.GetExec() != nil:
		sent := wire.GetExec()
		read := make(map[envvars.Cell]envsource.Value, len(sent.GetValues()))
		for _, value := range sent.GetValues() {
			at := envvars.Cell{Folder: value.GetCell().GetFolder(), Key: value.GetCell().GetKey()}
			read[at] = envsource.Value{Plaintext: []byte(value.GetValue()), Version: value.GetVersion()}
		}
		return envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{Command: sent.GetCommand()}}, read
	}
	return envsource.Descriptor{Kind: envsource.Builtin}, nil
}

func decodeAuth(method *envvarsv1.InfisicalAuth) envsource.InfisicalAuth {
	switch {
	case method.GetUniversal() != nil:
		return envsource.InfisicalAuth{
			Method:               envsource.AuthUniversal,
			ClientIDVariable:     method.GetUniversal().GetClientIdVariable(),
			ClientSecretVariable: method.GetUniversal().GetClientSecretVariable(),
		}
	case method.GetIdentity() != nil:
		return envsource.InfisicalAuth{Method: envsource.AuthIdentity, IdentityID: method.GetIdentity().GetIdentityId()}
	}
	return envsource.InfisicalAuth{}
}

func compareCells(a, b envvars.Cell) int {
	return cmp.Or(cmp.Compare(a.Folder, b.Folder), cmp.Compare(a.Key, b.Key))
}

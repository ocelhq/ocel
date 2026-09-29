package envsourceproto

import (
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
			Project:     options.Project,
			Environment: options.Environment,
			Path:        options.Path,
			Host:        options.Host,
			Auth:        encodeAuth(options.Auth),
			Write:       writePolicies[options.Normalize().Write],
		}}}
	case descriptor.Kind == envsource.Exec && descriptor.Exec != nil:
		sent := &envvarsv1.ExecEnvSource{Command: descriptor.Exec.Command}
		for _, at := range slices.SortedFunc(maps.Keys(read), envvars.Cell.Compare) {
			sent.Values = append(sent.Values, &envvarsv1.EnvSourceValue{
				Cell:  &envvarsv1.Cell{Folder: at.Folder, Key: at.Key},
				Value: string(read[at].Plaintext),
			})
		}
		return &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Exec{Exec: sent}}
	}
	return &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Builtin{Builtin: &envvarsv1.BuiltinEnvSource{}}}
}

var writePolicies = map[envsource.WritePolicy]envvarsv1.WritePolicy{
	envsource.WriteNever:   envvarsv1.WritePolicy_WRITE_POLICY_NEVER,
	envsource.WriteMissing: envvarsv1.WritePolicy_WRITE_POLICY_MISSING,
	envsource.WriteValues:  envvarsv1.WritePolicy_WRITE_POLICY_VALUES,
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

func Decode(message *envvarsv1.EnvSource) (envsource.Descriptor, map[envvars.Cell]envsource.Value) {
	switch {
	case message.GetInfisical() != nil:
		sent := message.GetInfisical()
		write := envsource.WriteNever
		for policy, encoded := range writePolicies {
			if encoded == sent.GetWrite() {
				write = policy
			}
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
	case message.GetExec() != nil:
		sent := message.GetExec()
		read := make(map[envvars.Cell]envsource.Value, len(sent.GetValues()))
		for _, value := range sent.GetValues() {
			at := envvars.Cell{Folder: value.GetCell().GetFolder(), Key: value.GetCell().GetKey()}
			read[at] = envsource.Value{Plaintext: []byte(value.GetValue())}
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

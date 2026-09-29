package envsourceproto

import (
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/envsource"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func Encode(descriptor envsource.Descriptor, read map[variablestore.Cell]envsource.Value) *variablestorev1.EnvSource {
	switch {
	case descriptor.Kind == envsource.Infisical && descriptor.Infisical != nil:
		options := descriptor.Infisical
		return &variablestorev1.EnvSource{Kind: &variablestorev1.EnvSource_Infisical{Infisical: &variablestorev1.InfisicalEnvSource{
			Project:     options.Project,
			Environment: options.Environment,
			Path:        options.Path,
			Host:        options.Host,
			Auth:        encodeAuth(options.Auth),
			Write:       writePolicies[options.Normalize().Write],
		}}}
	case descriptor.Kind == envsource.Exec && descriptor.Exec != nil:
		sent := &variablestorev1.ExecEnvSource{Command: descriptor.Exec.Command}
		for _, at := range slices.SortedFunc(maps.Keys(read), variablestore.Cell.Compare) {
			sent.Values = append(sent.Values, &variablestorev1.EnvSourceValue{
				Cell:  &variablestorev1.Cell{Folder: at.Folder, Key: at.Key},
				Value: string(read[at].Plaintext),
			})
		}
		return &variablestorev1.EnvSource{Kind: &variablestorev1.EnvSource_Exec{Exec: sent}}
	}
	return &variablestorev1.EnvSource{Kind: &variablestorev1.EnvSource_Builtin{Builtin: &variablestorev1.BuiltinEnvSource{}}}
}

var writePolicies = map[envsource.WritePolicy]variablestorev1.WritePolicy{
	envsource.WriteNever:   variablestorev1.WritePolicy_WRITE_POLICY_NEVER,
	envsource.WriteMissing: variablestorev1.WritePolicy_WRITE_POLICY_MISSING,
	envsource.WriteValues:  variablestorev1.WritePolicy_WRITE_POLICY_VALUES,
}

func encodeAuth(auth envsource.InfisicalAuth) *variablestorev1.InfisicalAuth {
	switch auth.Method {
	case envsource.AuthIdentity:
		return &variablestorev1.InfisicalAuth{Method: &variablestorev1.InfisicalAuth_Identity{Identity: &variablestorev1.InfisicalIdentityAuth{IdentityId: auth.IdentityID}}}
	case envsource.AuthUniversal:
		return &variablestorev1.InfisicalAuth{Method: &variablestorev1.InfisicalAuth_Universal{Universal: &variablestorev1.InfisicalUniversalAuth{
			ClientIdVariable:     auth.ClientIDVariable,
			ClientSecretVariable: auth.ClientSecretVariable,
		}}}
	}
	return nil
}

func Decode(message *variablestorev1.EnvSource) (envsource.Descriptor, map[variablestore.Cell]envsource.Value) {
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
		read := make(map[variablestore.Cell]envsource.Value, len(sent.GetValues()))
		for _, value := range sent.GetValues() {
			at := variablestore.Cell{Folder: value.GetCell().GetFolder(), Key: value.GetCell().GetKey()}
			read[at] = envsource.Value{Plaintext: []byte(value.GetValue())}
		}
		return envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{Command: sent.GetCommand()}}, read
	}
	return envsource.Descriptor{Kind: envsource.Builtin}, nil
}

func decodeAuth(method *variablestorev1.InfisicalAuth) envsource.InfisicalAuth {
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

package envsourceproto_test

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envsourceproto"
	"github.com/ocelhq/ocel/pkg/envvars"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func TestAnEnvSourceDecodesToWhatWasEncoded(t *testing.T) {
	t.Parallel()

	infisical := func(auth envsource.InfisicalAuth, write envsource.WritePolicy) envsource.Descriptor {
		return envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{
			Project: "p-1", Environment: "prod", Path: "/apps", Host: "https://infisical.example", Auth: auth, Write: write,
		}}
	}
	for _, c := range []struct {
		name       string
		descriptor envsource.Descriptor
		read       map[envvars.Cell]envsource.Value
	}{
		{name: "builtin", descriptor: envsource.Descriptor{Kind: envsource.Builtin}},
		{name: "infisical with universal auth that may create a missing key", descriptor: infisical(envsource.InfisicalAuth{
			Method: envsource.AuthUniversal, ClientIDVariable: "INFISICAL_CLIENT_ID", ClientSecretVariable: "INFISICAL_CLIENT_SECRET",
		}, envsource.WriteMissing)},
		{name: "infisical that may create or update a value", descriptor: infisical(envsource.InfisicalAuth{Method: envsource.AuthIdentity, IdentityID: "id-1"}, envsource.WriteValues)},
		{name: "infisical logging in as the target's cloud identity", descriptor: infisical(envsource.InfisicalAuth{Method: envsource.AuthIdentity, IdentityID: "id-1"}, envsource.WriteNever)},
		{
			name:       "exec with what its command printed",
			descriptor: envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{Command: []string{"vault", "export", "{folder}"}}},
			read: map[envvars.Cell]envsource.Value{
				{Key: "DATABASE_URL"}:               {Plaintext: []byte("postgres://db")},
				{Folder: "/web", Key: "STRIPE_KEY"}: {Plaintext: []byte("sk_live")},
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			descriptor, read := envsourceproto.Decode(envsourceproto.Encode(c.descriptor, c.read))
			if !reflect.DeepEqual(descriptor, c.descriptor) {
				t.Errorf("descriptor = %+v, want %+v", descriptor, c.descriptor)
			}
			if len(read) != len(c.read) || (len(c.read) > 0 && !reflect.DeepEqual(read, c.read)) {
				t.Errorf("read = %+v, want %+v", read, c.read)
			}
		})
	}
}

func TestAnExecValueArrivesWithNoVersionItsSenderChose(t *testing.T) {
	t.Parallel()
	sent := envsourceproto.Encode(envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{Command: []string{"true"}}}, map[envvars.Cell]envsource.Value{
		{Key: "A"}: {Plaintext: []byte("1"), Version: "s1@999"},
	})

	if _, read := envsourceproto.Decode(sent); read[envvars.Cell{Key: "A"}].Version != "" {
		t.Errorf("A decoded at version %q, want none: only the store it is copied into versions a value", read[envvars.Cell{Key: "A"}].Version)
	}
}

func TestADecodedInfisicalEnvSourceIsNormalized(t *testing.T) {
	t.Parallel()
	sent := envsourceproto.Encode(envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{
		Project: "p-1", Environment: "prod", Auth: envsource.InfisicalAuth{Method: envsource.AuthIdentity, IdentityID: "id"},
	}}, nil)

	if sent.GetInfisical().GetWrite() != envvarsv1.WritePolicy_WRITE_POLICY_NEVER {
		t.Errorf("write = %v, want never sent for a write left off", sent.GetInfisical().GetWrite())
	}
	descriptor, _ := envsourceproto.Decode(sent)
	if got := *descriptor.Infisical; got.Path != "/" || got.Host != "https://app.infisical.com" || got.Write != envsource.WriteNever {
		t.Errorf("options = %+v, want the root path, Infisical's cloud and write never filled in", got)
	}
}

func TestAnExecEnvSourceSendsItsValuesInFolderThenKeyOrder(t *testing.T) {
	t.Parallel()
	sent := envsourceproto.Encode(envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{Command: []string{"true"}}}, map[envvars.Cell]envsource.Value{
		{Folder: "/web", Key: "A"}: {Plaintext: []byte("3")},
		{Key: "B"}:                 {Plaintext: []byte("2")},
		{Key: "A"}:                 {Plaintext: []byte("1")},
	})

	var got []string
	for _, value := range sent.GetExec().GetValues() {
		got = append(got, value.GetCell().GetFolder()+" "+value.GetCell().GetKey())
	}
	if want := []string{" A", " B", "/web A"}; !reflect.DeepEqual(got, want) {
		t.Errorf("values = %q, want %q", got, want)
	}
}

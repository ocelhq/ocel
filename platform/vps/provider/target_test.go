package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func TestTheDestinationReadsWhatWasAuthored(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		options provider.Options
		want    vps.Target
	}{
		"a bare string names an ssh_config alias": {
			options: provider.Options{"ssh": "prod-box"},
			want:    vps.Target{Alias: "prod-box"},
		},
		"an object spells the destination out": {
			options: provider.Options{"ssh": map[string]any{
				"host":         "203.0.113.10",
				"port":         2222,
				"user":         "deploy",
				"identityFile": "~/.ssh/id_ed25519",
			}},
			want: vps.Target{
				Host:         "203.0.113.10",
				Port:         2222,
				User:         "deploy",
				IdentityFile: "~/.ssh/id_ed25519",
			},
		},
		"a host alone leaves the port and user unset": {
			options: provider.Options{"ssh": map[string]any{"host": "203.0.113.10"}},
			want:    vps.Target{Host: "203.0.113.10"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, err := vps.New(context.Background(), provider.Settings{Options: tc.options})
			if err != nil {
				t.Fatalf("New() = %v, want %s accepted", err, name)
			}

			if got := p.(*vps.Provider).Target(); got != tc.want {
				t.Errorf("Target() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTheDestinationRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		options provider.Options
		mention string
	}{
		"an unknown key": {
			options: provider.Options{"ssh": map[string]any{"host": "203.0.113.10", "hostname": "203.0.113.10"}},
			mention: `"provider.vps.ssh.hostname" is not a key this config has`,
		},
		"a port that is not a number": {
			options: provider.Options{"ssh": map[string]any{"host": "203.0.113.10", "port": "2222"}},
			mention: `"provider.vps.ssh.port" must be a number`,
		},
		"an object with no host": {options: provider.Options{"ssh": map[string]any{"user": "deploy"}}},
		"an empty alias":         {options: provider.Options{"ssh": ""}},
		"a whitespace alias":     {options: provider.Options{"ssh": "   "}},
		"a whitespace host":      {options: provider.Options{"ssh": map[string]any{"host": "  "}}},
		"no ssh key at all":      {options: provider.Options{}},
		"a null destination": {
			options: provider.Options{"ssh": nil},
			mention: "names no machine",
		},
		"a negative port": {
			options: provider.Options{"ssh": map[string]any{"host": "203.0.113.10", "port": -1}},
			mention: "outside 1-65535",
		},
		"a port past the end of the range": {
			options: provider.Options{"ssh": map[string]any{"host": "203.0.113.10", "port": 70000}},
			mention: "outside 1-65535",
		},
		"a number": {options: provider.Options{"ssh": 22}},
		"an array": {options: provider.Options{"ssh": []any{"203.0.113.10"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := vps.New(context.Background(), provider.Settings{Options: tc.options})
			if err == nil {
				t.Fatalf("New() with %s = nil, want a refusal", name)
			}
			if tc.mention != "" && !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("New() with %s = %v, want it to mention %q", name, err, tc.mention)
			}
		})
	}
}

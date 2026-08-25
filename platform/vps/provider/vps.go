package vps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Vendor providerkit.Vendor = "vps"

type Options struct {
	SSH Target `json:"ssh"`
}

type Target struct {
	Alias        string `json:"-"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	IdentityFile string `json:"identityFile"`
}

func (t *Target) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	switch {
	case bytes.Equal(trimmed, []byte("null")):
		return nil
	case trimmed[0] == '"':
		var alias string
		if err := json.Unmarshal(trimmed, &alias); err != nil {
			return err
		}
		*t = Target{Alias: alias}
		return nil
	case trimmed[0] == '{':
		type wire Target
		var decoded wire
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&decoded); err != nil {
			return fmt.Errorf(`option "ssh": %s`, spelledProblem(err))
		}
		*t = Target(decoded)
		return nil
	default:
		return errors.New(`option "ssh" is either an ssh_config alias or the destination spelled out`)
	}
}

func spelledProblem(err error) string {
	if key, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return "unknown key " + key
	}
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) && mismatch.Field != "" {
		return fmt.Sprintf("%q is not a %s", mismatch.Field, mismatch.Type)
	}
	return err.Error()
}

type Provider struct {
	options   Options
	machine   *host
	records   hostRecords
	artifacts *fake.Artifacts
	sealer    *fake.Sealer
}

func New(_ context.Context, options providerkit.Options) (providerkit.Provider, error) {
	decoded, err := providerkit.Decode[Options](options)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(decoded.SSH.Alias) == "" && strings.TrimSpace(decoded.SSH.Host) == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "option %q names no machine: give it an ssh_config alias, or an object with a host", "ssh")
	}
	if port := decoded.SSH.Port; port != 0 && (port < 1 || port > 65535) {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "option %q names port %d, which is outside 1-65535", "ssh", port)
	}
	return NewProvider(decoded), nil
}

func NewProvider(options Options) *Provider {
	machine := &host{target: options.SSH}
	return &Provider{
		options:   options,
		machine:   machine,
		records:   hostRecords{machine: machine},
		artifacts: fake.NewArtifacts(),
		sealer:    fake.NewSealer(),
	}
}

func (p *Provider) Vendor() providerkit.Vendor { return Vendor }

func (p *Provider) Target() Target { return p.options.SSH }

func (p *Provider) Serves() []providerkit.LinkType { return nil }

func (p *Provider) Bootstrap(edge.Kind) (providerkit.Bootstrapper, error) {
	return hostBootstrapper{machine: p.machine}, nil
}

func (p *Provider) Releases() providerkit.Releaser { return releaser{} }

func (p *Provider) Artifacts() providerkit.ArtifactStore { return p.artifacts }

func (p *Provider) Records() providerkit.RecordStore { return p.records }

func (p *Provider) Sealer() providerkit.Sealer { return p.sealer }

func (p *Provider) Credentials() providerkit.Credentials { return hostCredentials{machine: p.machine} }

func (p *Provider) Edges() providerkit.EdgeRegistry { return edges{} }

func (p *Provider) DNS() providerkit.DNSRegistry { return dns{} }

var _ providerkit.Provider = (*Provider)(nil)

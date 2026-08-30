package vps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const Vendor providerkit.Vendor = "vps"

type Options struct {
	SSH       Target `json:"ssh"`
	DeployKey string `json:"deployKey"`
}

type Target struct {
	Alias        string `json:"-"`
	Config       string `json:"-"`
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
	host      *host.Host
	records   *host.Records
	artifacts *fake.Artifacts
	sealer    *host.Sealer
	dial      sync.Mutex
	live      *session.Session
}

func (p *Provider) Session(ctx context.Context) (*session.Session, error) {
	p.dial.Lock()
	defer p.dial.Unlock()
	if p.live != nil {
		return p.live, nil
	}
	live, err := session.Open(ctx, session.Target{
		Alias:        p.options.SSH.Alias,
		Config:       p.options.SSH.Config,
		Host:         p.options.SSH.Host,
		Port:         p.options.SSH.Port,
		User:         p.options.SSH.User,
		IdentityFile: p.options.SSH.IdentityFile,
	})
	if err != nil {
		return nil, err
	}
	p.live = live
	return p.live, nil
}

func (p *Provider) conn(ctx context.Context) (host.Conn, error) {
	live, err := p.Session(ctx)
	if err != nil {
		return nil, err
	}
	return live, nil
}

func (p *Provider) Close() error {
	p.dial.Lock()
	defer p.dial.Unlock()
	live := p.live
	p.live = nil
	if live == nil {
		return nil
	}
	return live.Close()
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
	p := &Provider{
		options:   options,
		artifacts: fake.NewArtifacts(),
	}
	p.host = host.New(p.conn, host.Keys{Path: options.DeployKey})
	p.records = host.NewRecords(p.host)
	p.sealer = host.NewSealer(p.host)
	return p
}

func (p *Provider) Vendor() providerkit.Vendor { return Vendor }

func (p *Provider) Target() Target { return p.options.SSH }

func (p *Provider) Serves() []providerkit.LinkType { return resources.Serves(p) }

func (p *Provider) Computes() []providerkit.Compute {
	return []providerkit.Compute{providerkit.ComputeContainer}
}

func (p *Provider) Bootstrap(edge.Kind) (providerkit.Bootstrapper, error) {
	return host.Bootstrap(p.host), nil
}

func (p *Provider) Releases() providerkit.Releaser {
	return resources.Releaser(p.records, p.artifacts, p)
}

func (p *Provider) Artifacts() providerkit.ArtifactStore { return p.artifacts }

func (p *Provider) Records() providerkit.RecordStore { return p.records }

func (p *Provider) Sealer() providerkit.Sealer { return p.sealer }

func (p *Provider) Credentials() providerkit.Credentials { return credentials{p} }

func (p *Provider) Edges() providerkit.EdgeRegistry { return edges{} }

func (p *Provider) DNS() providerkit.DNSRegistry { return dns{} }

var _ providerkit.Provider = (*Provider)(nil)

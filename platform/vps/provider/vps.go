package vps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const Vendor providerkit.Vendor = "vps"

type Options struct {
	SSH          Target            `json:"ssh"`
	DeployKey    string            `json:"deployKey"`
	Certificates map[string]string `json:"certificates"`
}

type Target struct {
	Alias        string `json:"-"`
	Config       string `json:"-"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	IdentityFile string `json:"identityFile"`
}

func (t Target) session() session.Target {
	return session.Target{
		Alias:        t.Alias,
		Config:       t.Config,
		Host:         t.Host,
		Port:         t.Port,
		User:         t.User,
		IdentityFile: t.IdentityFile,
	}
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
	options Options
	host    *host.Host
	records *host.Records
	sealer  *host.Sealer
	probing *http.Client

	probed    sync.Mutex
	unreached map[string]string

	dial sync.Mutex
	live *session.Session
}

var _ providerkit.Diagnoser = (*Provider)(nil)

func (p *Provider) Session(ctx context.Context) (*session.Session, error) {
	p.dial.Lock()
	defer p.dial.Unlock()
	if p.live != nil {
		return p.live, nil
	}
	live, err := session.Open(ctx, p.options.SSH.session())
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
	p := &Provider{options: options}
	return p.standing(p.conn)
}

func newProvider(options Options, dial host.Dial) *Provider {
	return (&Provider{options: options}).standing(dial)
}

func (p *Provider) standing(dial host.Dial) *Provider {
	p.host = host.New(dial, host.Keys{Path: p.options.DeployKey}, pins(p.options.Certificates))
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
	return resources.Releaser(p.records, p.Artifacts(), p)
}

func (p *Provider) Artifacts() providerkit.ArtifactStore { return providerkit.NoArtifacts{} }

func (p *Provider) Records() providerkit.RecordStore { return p.records }

func (p *Provider) Sealer() providerkit.Sealer { return p.sealer }

func (p *Provider) Credentials() providerkit.Credentials { return credentials{p} }

func (p *Provider) Edges() providerkit.EdgeRegistry { return edges{p} }

func (p *Provider) box() *box.Edge {
	return box.New(p.host, p.records, p.options.SSH.session().Destination())
}

func pins(configured map[string]string) []host.Pin {
	held := make([]host.Pin, 0, len(configured))
	for hostname, path := range configured {
		held = append(held, host.Pin{Hostname: hostname, Path: strings.TrimSuffix(path, "/")})
	}
	slices.SortFunc(held, func(a, b host.Pin) int { return strings.Compare(a.Hostname, b.Hostname) })
	return held
}

func (p *Provider) DNS() providerkit.DNSRegistry { return dns{} }

var _ providerkit.Provider = (*Provider)(nil)

var _ providerkit.Prober = (*Provider)(nil)

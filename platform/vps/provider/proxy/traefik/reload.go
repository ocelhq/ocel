package traefik

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const reloadWait = reloadPauses * reloadInterval

func (t Traefik) reload(ctx context.Context, served proxy.Spec) error {
	spec, err := t.Box.ReadSpec(ctx)
	if err != nil {
		return err
	}
	waiting := t.changedHostnames(served, spec)
	failures := map[string]string{}
	for paused := 0; len(waiting) > 0; paused++ {
		var still []string
		for _, hostname := range waiting {
			routed, reason, err := t.probeServed(ctx, spec, hostname)
			if err != nil {
				return err
			}
			if !routed {
				failures[hostname] = reason
				still = append(still, hostname)
			}
		}
		if waiting = still; len(waiting) == 0 || paused == reloadPauses {
			break
		}
		if err := t.Box.Pause(ctx, reloadInterval); err != nil {
			return err
		}
	}
	if len(waiting) == 0 {
		return nil
	}
	said := make([]string, 0, len(waiting))
	for _, hostname := range waiting {
		said = append(said, failures[hostname])
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"your Traefik did not route %s to ocel's switchboard within %s:\n%s%s",
		strings.Join(waiting, ", "), reloadWait, strings.Join(said, "\n"), t.diagnose(ctx))
}

func (t Traefik) changedHostnames(served, spec proxy.Spec) []string {
	var changed []string
	for _, hostname := range spec.Hostnames {
		servedHTTPS, servedHTTP := t.routersFor(hostname, served)
		https, http := t.routersFor(hostname, spec)
		servedShield, _ := served.ShieldOf(hostname)
		shield, _ := spec.ShieldOf(hostname)
		if !slices.Contains(served.Hostnames, hostname) || !reflect.DeepEqual(servedHTTPS, https) || !reflect.DeepEqual(servedHTTP, http) ||
			!reflect.DeepEqual(servedShield, shield) {
			changed = append(changed, hostname)
		}
	}
	return changed
}

func (t Traefik) probeServed(ctx context.Context, spec proxy.Spec, hostname string) (bool, string, error) {
	if _, shielded := spec.ShieldOf(hostname); shielded {
		return t.probeShield(ctx, hostname)
	}
	return t.probeRoute(ctx, hostname)
}

func (t Traefik) probeShield(ctx context.Context, hostname string) (bool, string, error) {
	answered, failure, err := t.Box.ProbeAnyCertificate(ctx, hostname)
	switch {
	case err != nil:
		return false, "", err
	case answered != "":
		return false, fmt.Sprintf("%s answers on this box's 443 to a client that presents no certificate, so the shield in %s is not in effect", hostname, FileName), nil
	default:
		return true, failure, nil
	}
}

func (t Traefik) probeRoute(ctx context.Context, hostname string) (bool, string, error) {
	answered, failure, err := t.Box.ProbeAnyCertificate(ctx, hostname)
	switch {
	case err != nil:
		return false, "", err
	case failure != "":
		return false, failure, nil
	case answered != string(switchboard.RouterKind):
		return false, fmt.Sprintf("%s answers on this box's 443 as %q, not through ocel's switchboard", hostname, answered), nil
	default:
		return true, "", nil
	}
}

const (
	coolifyProxy     = "coolify-proxy"
	caddyDockerProxy = "caddy-docker-proxy"
)

func (t Traefik) diagnose(ctx context.Context) string {
	image, err := t.Box.Ran(ctx, "ask what "+coolifyProxy+" runs", coolifyProxyImage())
	if err != nil {
		return fmt.Sprintf("\nask what %s runs: %v", coolifyProxy, err)
	}
	if strings.Contains(image, caddyDockerProxy) {
		return fmt.Sprintf("\n%s runs %s: Coolify has switched this box to Caddy, which reads nothing in %s\n"+
			"Write `\"proxy\": { \"caddy\": { \"preset\": \"coolify\" } }` in this project's vps options, or switch Coolify back to Traefik",
			coolifyProxy, strings.TrimSpace(image), t.directory())
	}
	beside, err := t.Box.ReadBeside(ctx, t.file())
	if err != nil {
		return fmt.Sprintf("\nread the files beside %s: %v", t.file(), err)
	}
	slices.SortFunc(beside, func(a, b switchboard.SiblingFile) int { return strings.Compare(a.Name, b.Name) })
	for _, file := range beside {
		if err := parseLikeTraefik(file.Name, file.Content); err != nil && !errors.Is(err, errOnlyTraefikSees) {
			at := filepath.Join(t.directory(), file.Name)
			return fmt.Sprintf("\n%s does not parse: %v\nWhile it does, your Traefik takes up no change to any file in %s, ocel's among them, and after a restart it serves none of them\nFix or remove %s",
				at, oneLine(err), t.directory(), at)
		}
	}
	return ""
}

var knownSections = map[string][]string{
	"http": {"routers", "middlewares", "services", "serverstransports"},
	"tcp":  {"routers", "middlewares", "services", "serverstransports"},
	"udp":  {"routers", "services"},
	"tls":  {"certificates", "options", "stores"},
}

func parseLikeTraefik(name string, content []byte) error {
	tree, err := decodeDynamic(name, content)
	if err != nil {
		return err
	}
	for root, value := range tree {
		known, section := knownSections[strings.ToLower(root)]
		if !section || value == nil {
			continue
		}
		children, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not a mapping", root)
		}
		for _, child := range slices.Sorted(maps.Keys(children)) {
			if !slices.Contains(known, strings.ToLower(child)) {
				return fmt.Errorf("%s.%s is no field Traefik knows", root, child)
			}
		}
	}
	return nil
}

func oneLine(err error) string { return strings.Join(strings.Fields(err.Error()), " ") }

package traefik

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const reloadWait = reloadPauses * reloadInterval

func (t Traefik) reload(ctx context.Context) error {
	spec, err := t.Box.Spec(ctx)
	if err != nil {
		return err
	}
	waiting := slices.Clone(spec.Hostnames)
	failures := map[string]string{}
	for paused := 0; len(waiting) > 0; paused++ {
		var still []string
		for _, hostname := range waiting {
			failure, err := t.routed(ctx, hostname)
			if err != nil {
				return err
			}
			if failure != "" {
				failures[hostname] = failure
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
		strings.Join(waiting, ", "), reloadWait, strings.Join(said, "\n"), t.diagnosed(ctx))
}

func (t Traefik) routed(ctx context.Context, hostname string) (string, error) {
	answered, failure, err := t.Box.Routed(ctx, hostname)
	switch {
	case err != nil:
		return "", err
	case failure != "":
		return failure, nil
	case answered != string(switchboard.RouterKind):
		return fmt.Sprintf("%s answers on this box's 443 as %q, not through ocel's switchboard", hostname, answered), nil
	default:
		return "", nil
	}
}

const (
	coolifyProxy     = "coolify-proxy"
	caddyDockerProxy = "caddy-docker-proxy"
)

func (t Traefik) diagnosed(ctx context.Context) string {
	image, err := t.Box.Ran(ctx, "ask what "+coolifyProxy+" runs", coolifyProxyImage())
	if err != nil {
		return fmt.Sprintf("\nask what %s runs: %v", coolifyProxy, err)
	}
	if strings.Contains(image, caddyDockerProxy) {
		return fmt.Sprintf("\n%s runs %s: Coolify has switched this box to Caddy, which reads nothing in %s\n"+
			"Write `\"proxy\": { \"caddy\": { \"preset\": \"coolify\" } }` in this project's vps options, or switch Coolify back to Traefik",
			coolifyProxy, strings.TrimSpace(image), t.Directory)
	}
	beside, err := t.Box.Beside(ctx, t.file())
	if err != nil {
		return fmt.Sprintf("\nread the files beside %s: %v", t.file(), err)
	}
	slices.SortFunc(beside, func(a, b switchboard.Neighbour) int { return strings.Compare(a.Name, b.Name) })
	for _, file := range beside {
		if err := parses(file.Name, file.Content); err != nil {
			at := filepath.Join(filepath.Clean(t.Directory), file.Name)
			return fmt.Sprintf("\n%s does not parse: %v\nWhile it does, your Traefik takes up no change to any file in %s, ocel's among them, and after a restart it serves none of them\nFix or remove %s",
				at, oneLine(err), filepath.Clean(t.Directory), at)
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

func parses(name string, content []byte) error {
	tree, err := decoded(name, content)
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

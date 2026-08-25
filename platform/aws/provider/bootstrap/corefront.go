package bootstrap

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type CoreFragment struct {
	Resources string
	Outputs   string
}

type CoreFront interface {
	CoreStack(class string) CoreFragment
}

func coreFragment(front edge.Edge, class string) CoreFragment {
	resident, ok := front.(CoreFront)
	if !ok {
		return CoreFragment{}
	}
	return resident.CoreStack(class)
}

func refuseEdgeSwitch(target spec, front edge.Edge, standing Deployed) error {
	if !standing.Present {
		return nil
	}
	held := edgeOutputsHeld(target, standing.CoreOutputs)
	wanted := edgeOutputsOf(target, coreFragment(front, target.class))
	if standsAgainst(held, wanted) {
		return nil
	}
	return fmt.Errorf(
		"the %s bootstrap standing in this account was written against a different edge: its core stack carries %s, "+
			"where the %q edge fronts deployments with %s. An account fronts its deployments with one edge, and moving it "+
			"to another is a destroy and a fresh bootstrap, not an upgrade: tear the deployments down, run "+
			"`ocel bootstrap destroy %s`, then `%s` with this edge selected",
		target.class, outputsNamed(held), front.Kind(), outputsNamed(wanted), target.class,
		providerkit.BootstrapCommand(edge.Class(target.class)))
}

func standsAgainst(held, wanted []string) bool {
	if len(held) == 0 || len(wanted) == 0 {
		return len(held) == len(wanted)
	}
	for _, key := range wanted {
		if slices.Contains(held, key) {
			return true
		}
	}
	return false
}

func outputsNamed(keys []string) string {
	if len(keys) == 0 {
		return "nothing an edge fronts with"
	}
	return strings.Join(keys, ", ")
}

func edgeOutputsOf(target spec, fragment CoreFragment) []string {
	own := templateOutputKeys(target.core(fragment))
	return withoutCoreOutputs(target, own)
}

func edgeOutputsHeld(target spec, standing map[string]string) []string {
	held := make([]string, 0, len(standing))
	for key := range standing {
		held = append(held, key)
	}
	return withoutCoreOutputs(target, held)
}

func withoutCoreOutputs(target spec, keys []string) []string {
	core := templateOutputKeys(target.core(CoreFragment{}))
	keys = slices.DeleteFunc(keys, func(key string) bool { return slices.Contains(core, key) })
	slices.Sort(keys)
	return keys
}

func Indent(body string, by int) string {
	pad := strings.Repeat(" ", by)
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	var b strings.Builder
	for _, line := range lines {
		if line != "" {
			b.WriteString(pad)
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return b.String()
}

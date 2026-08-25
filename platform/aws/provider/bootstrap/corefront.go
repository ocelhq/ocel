package bootstrap

import (
	"strings"

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

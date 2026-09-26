package surface

import (
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func Name(ns bootstrap.Namespace, slug string, class edge.Class, tail ...string) string {
	return naming.Join(naming.FieldSeparator, append([]string{string(ns), slug, string(class)}, tail...)...)
}

func Fitted(max int, ns bootstrap.Namespace, slug string, class edge.Class, tail ...string) string {
	budget := max - len(Name(ns, "", class, tail...)) - len(naming.FieldSeparator)
	return Name(ns, naming.Fit(budget, naming.WordSeparator, naming.Compressible(slug)), class, tail...)
}

func ProjectsNamed(ns bootstrap.Namespace, names []string, class edge.Class) []string {
	var slugs []string
	for _, name := range names {
		fields := strings.Split(name, naming.FieldSeparator)
		if len(fields) < 3 || fields[0] != string(ns) || fields[2] != string(class) {
			continue
		}
		if !slices.Contains(slugs, fields[1]) {
			slugs = append(slugs, fields[1])
		}
	}
	return slugs
}

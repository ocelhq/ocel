package surface

import (
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
)

const MaxNameLen = 128

func Name(ns account.Namespace, slug string, tier environment.Tier, tail ...string) (string, error) {
	named := naming.TaggedName{
		Head:     naming.OcelName(string(ns), tier),
		Readable: naming.Join(naming.WordSeparator, append([]string{slug}, tail...)...),
		Tag:      naming.HashTag(append([]string{string(ns), string(tier), slug}, tail...)...),
	}
	name, err := named.FitWhole(MaxNameLen)
	if err != nil {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s does not fit the %d characters an edge names a project's surface in, and a surface cut short cannot be read back to the project that owns it\nShorten the project slug",
			named.Readable, MaxNameLen)
	}
	return name, nil
}

func ProjectsNamed(ns account.Namespace, names []string, tier environment.Tier) []string {
	head := naming.OcelName(string(ns), tier) + naming.WordSeparator
	var slugs []string
	for _, name := range names {
		rest, under := strings.CutPrefix(name, head)
		cut := strings.LastIndex(rest, naming.WordSeparator)
		if !under || cut < 0 {
			continue
		}
		slug := rest[:cut]
		if named, err := Name(ns, slug, tier); err != nil || named != name {
			continue
		}
		if !slices.Contains(slugs, slug) {
			slugs = append(slugs, slug)
		}
	}
	return slugs
}

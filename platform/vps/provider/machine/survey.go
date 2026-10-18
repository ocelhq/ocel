package machine

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

func SurveyScript(items []Item, also ...string) string {
	return SurveyingScript(`[ -f "$p" ]`, items, also...)
}

func SurveyingScript(file string, items []Item, also ...string) string {
	var probes, script strings.Builder
	script.WriteString("for p in")
	for _, item := range items {
		if probe := item.Probe(); probe != "" {
			probes.WriteString(probe + "\n")
			continue
		}
		script.WriteString(" " + shell.Quote(item.Name))
	}
	for _, path := range also {
		script.WriteString(" " + shell.Quote(path))
	}
	stated, owner := `"$(stat -c %a "$p")"`, `"$(stat -c %U "$p")"`
	script.WriteString(`; do
if [ -h "$p" ]; then ` + Reports(shell.Quote(KindLink), `"$p"`, `0`, `''`, `"$(readlink "$p")"`) + `
elif [ -d "$p" ]; then ` + Reports(shell.Quote(KindDir), `"$p"`, stated, owner, `''`) + `
elif ` + file + `; then ` + Reports(shell.Quote(KindFile), `"$p"`, stated, owner, `"$(sha256sum "$p" | cut -d' ' -f1)"`) + `
fi
done`)
	return probes.String() + script.String()
}

const (
	KindLink       = "fs:link"
	KindUnreadable = "probe:unreadable"
)

func Unreadable(kind, name, why string) string {
	return Reports(shell.Quote(KindUnreadable), name, "0", shell.Quote(kind), why)
}

func column(columns []string, at int, absent string) string {
	if at < len(columns) && columns[at] != "" {
		return columns[at]
	}
	return absent
}

func remainder(columns []string, separator, absent string) string {
	if len(columns) > 4 && columns[4] != "" {
		return strings.Join(columns[4:], separator)
	}
	return absent
}

const unnamedPath = "(unnamed)"

func couldNotLook(columns []string) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"could not check %s %s on this host: %s",
		column(columns, 3, "path"),
		column(columns, 1, unnamedPath),
		remainder(columns, " ", "no reason given"))
}

func pointedAway(columns []string) error {
	named := column(columns, 1, unnamedPath)
	return refusal.Refuse(refusal.CodeDenied,
		"%s is a symbolic link to %s\n"+
			"Put a real directory or file at %s",
		named, remainder(columns, "\t", "an unreadable target"), named)
}

func Reports(kind, name, mode, owner, sum string) string {
	return strings.Join([]string{`printf '%s\t%s\t%s\t%s\t%s\n'`, kind, name, mode, owner, sum}, " ")
}

func ReadSurvey(rendered string) (map[string]string, Seal, error) {
	observed := make(map[string]string)
	var seal Seal
	for _, line := range strings.Split(strings.Trim(rendered, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		columns := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if columns[0] == EngineFactsRow {
			continue
		}
		if columns[0] == KindLink {
			return nil, Seal{}, pointedAway(columns)
		}
		if columns[0] == KindUnreadable {
			return nil, Seal{}, couldNotLook(columns)
		}
		sealed := columns[0] == KindSealKey
		if (sealed && len(columns) != 6) || (!sealed && len(columns) != 5) {
			return nil, Seal{}, refusal.Refuse(refusal.CodeDenied,
				"the host answered a survey line ocel cannot read: %q", line)
		}
		parsed, err := ParseMode(columns[2])
		if err != nil {
			return nil, Seal{}, refusal.Refuse(refusal.CodeDenied,
				"the host reported %q as the mode of %s", columns[2], columns[1])
		}
		content := columns[4]
		if sealed {
			seal = Seal{Fingerprint: columns[4], Algorithm: SealAlgorithm, CreatedAt: columns[5]}
			content = ""
		}
		observed[columns[0]+" "+columns[1]] = DigestOf(columns[0], columns[1], parsed, columns[3], content)
	}
	return observed, seal, nil
}

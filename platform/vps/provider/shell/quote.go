package shell

import (
	"strings"
)

func Words(argv []string) string {
	written := make([]string, 0, len(argv))
	for _, arg := range argv {
		written = append(written, Quote(arg))
	}
	return strings.Join(written, " ")
}

func Quote(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

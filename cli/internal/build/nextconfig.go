package build

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

var nextConfigFiles = []string{"next.config.js", "next.config.mjs", "next.config.cjs", "next.config.ts", "next.config.mts"}

var ownAdapter = regexp.MustCompile(`\badapterPath\s*[:,}]`)

func RefuseNextFunctionsWithOwnAdapter(cfg *project.Project) error {
	var refusals []error
	for _, a := range FunctionApps(cfg.Apps) {
		if a.Framework() != buildoutput.FrameworkNext {
			continue
		}
		file, text, err := readNextConfig(filepath.Join(cfg.Dir, a.Path))
		if err != nil {
			return err
		}
		if ownAdapter.MatchString(stripJSComments(text)) {
			refusals = append(refusals, fmt.Errorf(`app %q sets adapterPath in %[2]s, and next build then runs that adapter in place of ocel's, which writes the output ocel deploys: delete adapterPath from %[2]s`, a.Name, file))
		}
	}
	return errors.Join(refusals...)
}

func readNextConfig(dir string) (file, text string, err error) {
	for _, name := range nextConfigFiles {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		return name, string(body), nil
	}
	return "", "", nil
}

func stripJSComments(src string) string {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0:
			out.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				out.WriteByte(src[i])
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
			out.WriteByte(c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			out.WriteByte('\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += end + 3
			out.WriteByte(' ')
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

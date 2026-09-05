package clientenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxExtends = 16

func withMapping(path, source, accessor string) (string, error) {
	if hasKey(source, specifier) {
		return source, nil
	}

	file, ok := parse(source)
	if !ok {
		return "", refuse(path, "", accessor, "ocel could not read it")
	}

	baseURL := file.baseURL
	if file.hasExtends && (!file.hasBaseURL || !file.hasPaths) {
		base, err := extended(path, file.extends)
		if err != nil {
			return "", refuse(path, baseURL, accessor, err.Error())
		}
		if !file.hasPaths {
			if base.mapped {
				return source, nil
			}
			if base.hasPaths {
				return "", refuse(path, baseURL, accessor, fmt.Sprintf(
					"it extends %q, which states compilerOptions.paths of its own, and TypeScript does not merge paths across extends — writing one here would replace every alias that config declares",
					file.extends))
			}
		}
		if !file.hasBaseURL {
			baseURL = base.baseURL
		}
	}

	target, err := accessorTarget(filepath.Dir(path), baseURL, accessor)
	if err != nil {
		return "", refuse(path, "", accessor, fmt.Sprintf("ocel could not resolve the accessor against its baseUrl %q", baseURL))
	}

	entry := fmt.Sprintf("%q: [%q]", specifier, target)
	switch {
	case file.hasPaths:
		return insertMember(source, file.paths, 3, entry), nil
	case file.hasOptions:
		return insertMember(source, file.options, 2, `"paths": { `+entry+` }`), nil
	default:
		return insertMember(source, file.root, 1, `"compilerOptions": { "paths": { `+entry+` } }`), nil
	}
}

func refuse(path, baseURL, accessor, reason string) error {
	target, err := accessorTarget(filepath.Dir(path), baseURL, accessor)
	if err != nil {
		target, _ = accessorTarget(filepath.Dir(path), "", accessor)
	}
	return fmt.Errorf("%s: %s, so the %q mapping was not written. Add it under compilerOptions.paths yourself:\n    %q: [%q]",
		filepath.Base(path), reason, specifier, specifier, target)
}

func accessorTarget(configDir, baseURL, accessor string) (string, error) {
	from := configDir
	if baseURL != "" {
		from = filepath.Join(configDir, filepath.FromSlash(baseURL))
	}
	target, err := filepath.Rel(from, accessor)
	if err != nil {
		return "", err
	}
	slashed := filepath.ToSlash(target)
	if strings.HasPrefix(slashed, "../") {
		return slashed, nil
	}
	return "./" + slashed, nil
}

type local struct {
	root       span
	options    span
	hasOptions bool
	paths      span
	hasPaths   bool
	baseURL    string
	hasBaseURL bool
	extends    string
	hasExtends bool
}

func parse(source string) (local, bool) {
	root, ok := objectBody(source, skipTrivia(source, 0))
	if !ok {
		return local{}, false
	}
	file := local{root: root}

	if value, ok := memberValue(source, root, "extends"); ok {
		file.hasExtends = true
		file.extends, _ = stringAt(source, value)
	}

	file.options, file.hasOptions = memberObject(source, root, "compilerOptions")
	if !file.hasOptions {
		return file, true
	}

	if value, ok := memberValue(source, file.options, "baseUrl"); ok {
		file.baseURL, file.hasBaseURL = stringAt(source, value)
	}
	if value, ok := memberValue(source, file.options, "paths"); ok {
		file.paths, file.hasPaths = objectBody(source, value)
		if !file.hasPaths {
			return local{}, false
		}
	}
	return file, true
}

type inherited struct {
	baseURL  string
	hasPaths bool
	mapped   bool
}

func extended(from, extends string) (inherited, error) {
	var out inherited
	found := false
	dir := filepath.Dir(from)

	for range maxExtends {
		if !strings.HasPrefix(extends, "./") && !strings.HasPrefix(extends, "../") {
			return inherited{}, fmt.Errorf("it extends %q, which ocel cannot resolve", extends)
		}
		path := filepath.Join(dir, filepath.FromSlash(extends))
		if filepath.Ext(path) == "" {
			path += ".json"
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return inherited{}, fmt.Errorf("it extends %q, which ocel cannot read", extends)
		}
		source := string(data)
		base, ok := parse(source)
		if !ok {
			return inherited{}, fmt.Errorf("it extends %q, which ocel could not read", extends)
		}

		if base.hasPaths {
			out.hasPaths = true
			out.mapped = out.mapped || hasKey(source, specifier)
		}
		if base.hasBaseURL && !found {
			rel, err := filepath.Rel(filepath.Dir(from), filepath.Join(filepath.Dir(path), filepath.FromSlash(base.baseURL)))
			if err != nil {
				return inherited{}, fmt.Errorf("it extends %q, whose baseUrl ocel cannot resolve", extends)
			}
			out.baseURL, found = rel, true
		}
		if !base.hasExtends {
			return out, nil
		}
		dir, extends = filepath.Dir(path), base.extends
	}
	return inherited{}, errors.New("its chain of extended configs is too long to follow")
}

type span struct{ start, end int }

func insertMember(source string, body span, depth int, member string) string {
	tail := ",\n"
	if skipTrivia(source, body.start) >= body.end {
		tail = "\n" + strings.Repeat("  ", depth-1)
	}
	return source[:body.start] + "\n" + strings.Repeat("  ", depth) + member + tail + source[body.start:]
}

func memberObject(source string, body span, key string) (span, bool) {
	value, ok := memberValue(source, body, key)
	if !ok {
		return span{}, false
	}
	return objectBody(source, value)
}

func memberValue(source string, body span, key string) (int, bool) {
	for i := body.start; i < body.end; {
		if source[i] == '"' {
			name, next, ok := readString(source, i)
			if !ok {
				return 0, false
			}
			after := skipTrivia(source, next)
			if after < body.end && source[after] == ':' && name == key {
				return skipTrivia(source, after+1), true
			}
			i = next
			continue
		}
		if source[i] == '{' || source[i] == '[' {
			nested, ok := valueEnd(source, i)
			if !ok {
				return 0, false
			}
			i = nested
			continue
		}
		i = advance(source, i)
	}
	return 0, false
}

func objectBody(source string, i int) (span, bool) {
	if i >= len(source) || source[i] != '{' {
		return span{}, false
	}
	end, ok := valueEnd(source, i)
	if !ok {
		return span{}, false
	}
	return span{start: i + 1, end: end - 1}, true
}

func valueEnd(source string, i int) (int, bool) {
	depth := 0
	for i < len(source) {
		switch source[i] {
		case '{', '[':
			depth++
			i++
		case '}', ']':
			depth--
			i++
			if depth == 0 {
				return i, true
			}
		case '"':
			_, next, ok := readString(source, i)
			if !ok {
				return 0, false
			}
			i = next
		default:
			i = advance(source, i)
		}
	}
	return 0, false
}

func hasKey(source, key string) bool {
	for i := 0; i < len(source); {
		if source[i] != '"' {
			i = advance(source, i)
			continue
		}
		name, next, ok := readString(source, i)
		if !ok {
			return false
		}
		after := skipTrivia(source, next)
		if name == key && after < len(source) && source[after] == ':' {
			return true
		}
		i = next
	}
	return false
}

func stringAt(source string, i int) (string, bool) {
	if i >= len(source) || source[i] != '"' {
		return "", false
	}
	value, _, ok := readString(source, i)
	return value, ok
}

func readString(source string, i int) (string, int, bool) {
	for j := i + 1; j < len(source); j++ {
		switch source[j] {
		case '\\':
			j++
		case '"':
			var value string
			if err := json.Unmarshal([]byte(source[i:j+1]), &value); err != nil {
				return "", 0, false
			}
			return value, j + 1, true
		}
	}
	return "", 0, false
}

func advance(source string, i int) int {
	if next, ok := commentEnd(source, i); ok {
		return next
	}
	return i + 1
}

const byteOrderMark = "\ufeff"

func skipTrivia(source string, i int) int {
	for i < len(source) {
		if next, ok := commentEnd(source, i); ok {
			i = next
			continue
		}
		if strings.HasPrefix(source[i:], byteOrderMark) {
			i += len(byteOrderMark)
			continue
		}
		switch source[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

func commentEnd(source string, i int) (int, bool) {
	if i+1 >= len(source) || source[i] != '/' {
		return 0, false
	}
	switch source[i+1] {
	case '/':
		if end := strings.IndexByte(source[i:], '\n'); end >= 0 {
			return i + end, true
		}
		return len(source), true
	case '*':
		if end := strings.Index(source[i+2:], "*/"); end >= 0 {
			return i + 2 + end + 2, true
		}
		return len(source), true
	}
	return 0, false
}

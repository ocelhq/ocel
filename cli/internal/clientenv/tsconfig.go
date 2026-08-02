package clientenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A tsconfig is a file a developer maintains — comments, trailing commas and
// all — so the entry is spliced into the text rather than parsed and written
// back. Re-serialising would silently drop everything JSON cannot hold.

// maxExtends bounds the chain of base configs followed, so a cycle is an error
// rather than a hang.
const maxExtends = 16

// withMapping returns source carrying a compilerOptions.paths entry that
// resolves specifier at the app's generated accessor, or source unchanged when
// the mapping is already there. path is where source was read from, because
// two things the entry depends on can live in another file: the baseUrl a
// paths value is resolved against, and the paths a base config already states.
//
// Where the entry cannot be written truthfully the config is left alone and
// the refusal says what to write by hand. The alternative — a mapping that
// resolves somewhere else, or one that replaces a base config's aliases — is a
// broken project reported as a success.
func withMapping(path, source string) (string, error) {
	if hasKey(source, specifier) {
		return source, nil
	}

	file, ok := parse(source)
	if !ok {
		return "", refuse(path, "", "ocel could not read it")
	}

	baseURL := file.baseURL
	if file.hasExtends && (!file.hasBaseURL || !file.hasPaths) {
		base, err := extended(path, file.extends)
		if err != nil {
			return "", refuse(path, baseURL, err.Error())
		}
		if !file.hasPaths {
			if base.mapped {
				return source, nil
			}
			if base.hasPaths {
				return "", refuse(path, baseURL, fmt.Sprintf(
					"it extends %q, which states compilerOptions.paths of its own, and TypeScript does not merge paths across extends — writing one here would replace every alias that config declares",
					file.extends))
			}
		}
		if !file.hasBaseURL {
			baseURL = base.baseURL
		}
	}

	target, err := accessorTarget(baseURL)
	if err != nil {
		return "", refuse(path, "", fmt.Sprintf("ocel could not resolve the accessor against its baseUrl %q", baseURL))
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

// refuse reports why a config was left alone, and states the entry the
// developer has to add for `ocel/env/client` to resolve.
func refuse(path, baseURL, reason string) error {
	target, err := accessorTarget(baseURL)
	if err != nil {
		target, _ = accessorTarget("")
	}
	return fmt.Errorf("%s: %s, so the %q mapping was not written. Add it under compilerOptions.paths yourself:\n    %q: [%q]",
		filepath.Base(path), reason, specifier, specifier, target)
}

// accessorTarget is what a paths value has to say to reach the generated
// accessor. TypeScript resolves a paths value against baseUrl where a config
// states one and against the config's own directory otherwise, so the target
// is derived from accessorPath rather than spelled a second time.
func accessorTarget(baseURL string) (string, error) {
	target := accessorPath
	if baseURL != "" {
		rel, err := filepath.Rel(filepath.FromSlash(baseURL), accessorPath)
		if err != nil {
			return "", err
		}
		target = rel
	}
	slashed := filepath.ToSlash(target)
	if strings.HasPrefix(slashed, "../") {
		return slashed, nil
	}
	return "./" + slashed, nil
}

// local is what one config file's own text states.
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

// parse reads the members the mapping depends on out of one config's text. It
// fails on anything it cannot account for — a file that is not an object, a
// paths member that is not one — rather than reporting an absence it did not
// establish.
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

// inherited is what a config's extends chain contributes: the baseUrl its
// paths values resolve against, whether any config in it states paths a child's
// own would replace, and whether one already maps the specifier.
type inherited struct {
	baseURL  string
	hasPaths bool
	mapped   bool
}

// extended follows the chain of base configs from, stated as extends, and
// reports what they contribute. Only a relative specifier is followed: a bare
// one is resolved by the package manager's rules, which the CLI does not
// implement and will not guess at.
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
			// A base config's relative paths are resolved against the directory
			// that config sits in, not the child's.
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

// span is a JSON object's body: the range between its braces.
type span struct{ start, end int }

// insertMember splices member in as an object's first entry, indented one
// level deeper than the line the object opens on. The separating comma goes
// after it, so an object that was empty does not gain a trailing one.
func insertMember(source string, body span, depth int, member string) string {
	tail := ",\n"
	if skipTrivia(source, body.start) >= body.end {
		tail = "\n" + strings.Repeat("  ", depth-1)
	}
	return source[:body.start] + "\n" + strings.Repeat("  ", depth) + member + tail + source[body.start:]
}

// memberObject returns the body of the object the named member of the object
// at body holds, if that member exists and is an object.
func memberObject(source string, body span, key string) (span, bool) {
	value, ok := memberValue(source, body, key)
	if !ok {
		return span{}, false
	}
	return objectBody(source, value)
}

// memberValue returns the index of the value the named member of the object at
// body holds. Only that object's own members are considered — a key of the
// same name nested inside one of its values is a different key.
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

// objectBody returns the range between the braces of the object starting at i.
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

// valueEnd returns the index just past the bracketed value starting at i.
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

// hasKey reports whether the source names key as a member anywhere in it, at
// any depth. Presence is the whole question: a mapping that exists is left
// alone wherever the developer put it.
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

// stringAt is the value of the string literal at i, if a string is what is
// there.
func stringAt(source string, i int) (string, bool) {
	if i >= len(source) || source[i] != '"' {
		return "", false
	}
	value, _, ok := readString(source, i)
	return value, ok
}

// readString reads the string literal starting at source[i], returning its
// value and the index just past its closing quote. The literal is decoded as
// JSON, so an escape means what the file says it means.
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

// advance steps over one byte, or over a whole comment where one starts.
func advance(source string, i int) int {
	if next, ok := commentEnd(source, i); ok {
		return next
	}
	return i + 1
}

// byteOrderMark is trivia too: a config that opens with one is a config whose
// first brace is three bytes in.
const byteOrderMark = "\ufeff"

// skipTrivia returns the index of the next byte that is neither whitespace nor
// part of a comment.
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

// commentEnd returns the index just past the comment starting at i, if one
// starts there. An unterminated block comment ends the file, which leaves
// whatever it was inside unclosed and is reported as a config that cannot be
// read.
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

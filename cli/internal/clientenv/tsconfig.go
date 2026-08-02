package clientenv

import "strings"

// A tsconfig is a file a developer maintains — comments, trailing commas and
// all — so the entry is spliced into the text rather than parsed and written
// back. Re-serialising would silently drop everything JSON cannot hold.

// withPathsEntry returns source carrying a compilerOptions.paths entry mapping
// specifier to target, and whether it had to add one. A file already naming
// the specifier is returned untouched: the mapping is there, and which file it
// points at is then the developer's statement, not ours.
func withPathsEntry(source, specifier, target string) (string, bool) {
	if hasKey(source, specifier) {
		return source, false
	}

	entry := `"` + specifier + `": ["` + target + `"]`

	root, ok := objectBody(source, skipTrivia(source, 0))
	if !ok {
		return source, false
	}

	if options, ok := memberObject(source, root, "compilerOptions"); ok {
		if paths, ok := memberObject(source, options, "paths"); ok {
			return insertMember(source, paths, "      ", entry), true
		}
		return insertMember(source, options, "    ", `"paths": { `+entry+` }`), true
	}
	return insertMember(source, root, "  ", `"compilerOptions": { "paths": { `+entry+` } }`), true
}

// span is a JSON object's body: the range between its braces.
type span struct{ start, end int }

// insertMember splices member in as an object's first entry, indented under
// its own line. The separating comma goes after it, so an object that was
// empty does not gain a trailing one.
func insertMember(source string, body span, indent, member string) string {
	tail := ",\n"
	if skipTrivia(source, body.start) >= body.end {
		tail = "\n" + indent[:len(indent)-2]
	}
	return source[:body.start] + "\n" + indent + member + tail + source[body.start:]
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

// readString reads the string literal starting at source[i], returning its
// value and the index just past its closing quote.
func readString(source string, i int) (string, int, bool) {
	var b strings.Builder
	for j := i + 1; j < len(source); j++ {
		switch source[j] {
		case '\\':
			if j+1 >= len(source) {
				return "", 0, false
			}
			b.WriteByte(source[j+1])
			j++
		case '"':
			return b.String(), j + 1, true
		default:
			b.WriteByte(source[j])
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

// skipTrivia returns the index of the next byte that is neither whitespace nor
// part of a comment.
func skipTrivia(source string, i int) int {
	for i < len(source) {
		if next, ok := commentEnd(source, i); ok {
			i = next
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
// starts there.
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

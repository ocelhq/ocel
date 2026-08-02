// Package dotenv reads the flat dotfile `ocel dev` resolves the project's root
// values from, so getting started needs no cloud account.
//
// `.env` is the file the framework already reads. Ocel adopted it rather than
// claimed it, so the parser takes the assignments whose keys Ocel could be
// asked for and ignores every other line: a line Ocel has no stake in is not a
// line Ocel may refuse to start over. What it does read has no layering and no
// `$VAR` interpolation, because either makes a value's origin something a
// reader has to reconstruct rather than read.
package dotenv

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// FileName is the dotfile, at the project root and nowhere else. Dev elects one
// leader per project root, so a file found relative to a working directory
// would hand a leader and a follower of the same project different values.
const FileName = ".env"

// keyPattern and reservedPrefixes are the SDK's own rule for a key defineEnv
// could name (packages/ocel/src/env/definition.ts). A line outside it belongs
// to whatever else reads this file. Ocel's own namespace is the whole of it: a
// name a bundler inlines or a provider injects is one a project may declare,
// and this file is where dev resolves its value from.
var keyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

var reservedPrefixes = []string{"OCEL_"}

// File is one parsed dotfile: every assignment whose key Ocel could be asked
// for, last one wins as the framework's own parser answers, and the 1-based
// number of every line that is not an assignment at all. Numbers, never the
// line — an unreadable line is exactly the shape a pasted token has.
type File struct {
	Values     map[string]string
	Unreadable []int
}

// Load parses dir's dotfile. An absent file is not an error: a project that
// requires no values must still run.
func Load(dir string) (File, error) {
	file, err := os.Open(filepath.Join(dir, FileName))
	if err != nil {
		if os.IsNotExist(err) {
			return File{Values: map[string]string{}}, nil
		}
		return File{}, fmt.Errorf("read %s: %w", FileName, err)
	}
	defer file.Close()

	parsed := File{Values: map[string]string{}}
	scanner := bufio.NewScanner(file)
	scanner.Split(splitLines)
	for line := 1; scanner.Scan(); line++ {
		key, value, readable := parseLine(scanner.Text())
		switch {
		case !readable:
			parsed.Unreadable = append(parsed.Unreadable, line)
		case key != "":
			parsed.Values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return File{}, fmt.Errorf("read %s: %w", FileName, err)
	}
	return parsed, nil
}

// splitLines ends a line at "\n", "\r\n", or a lone "\r", because the parser
// this file is shared with rewrites `\r\n?` to `\n` before it reads anything.
// Splitting only on "\n" let one key's value swallow the next key's whole line
// in a file written with classic-Mac endings — the second key silently absent
// and the first silently wrong. Line numbers count the lines the file has under
// its own endings, so what an unreadable line discloses stays a number a
// developer can find.
func splitLines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	end := bytes.IndexAny(data, "\r\n")
	switch {
	case end < 0 && atEOF:
		return len(data), data, nil
	case end < 0:
		return 0, nil, nil
	case data[end] == '\n':
		return end + 1, data[:end], nil
	case end == len(data)-1 && !atEOF:
		// A trailing "\r" may yet be the first half of a "\r\n".
		return 0, nil, nil
	case end+1 < len(data) && data[end+1] == '\n':
		return end + 2, data[:end], nil
	default:
		return end + 1, data[:end], nil
	}
}

// space is JS's `\s`, which is what decides where a key starts in the regex
// this parser mirrors. It differs from Go's own definition in exactly two
// characters, and both cost a value when they are got wrong. U+FEFF is the byte
// order mark a `.env` written by PowerShell redirection opens with: without it
// the mark became part of the first key, which then failed as undeclarable and
// took the file's first value with it, in silence. U+0085 goes the other way —
// Go calls it space and JS does not, so treating it as one would let Ocel read
// `export<U+0085>KEY=v` as an assignment the framework never sees.
func space(r rune) bool { return (unicode.IsSpace(r) && r != '\u0085') || r == '\ufeff' }

func trimSpace(s string) string { return strings.TrimFunc(s, space) }

// parseLine reports false for a line that assigns nothing, and an empty key for
// one that assigns something Ocel could not be asked for. The value half stops
// at the first unquoted '#': a quoted token — single, double, or backtick —
// keeps a value whole through one, but only while nothing follows the closing
// quote but whitespace and an optional comment. There is no '#' escape,
// matching the parser this file is shared with.
func parseLine(raw string) (string, string, bool) {
	line := trimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", true
	}

	name, rest, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}

	key := trimSpace(unexport(trimSpace(name)))
	if !declarable(key) {
		return "", "", true
	}
	return key, value(trimSpace(rest)), true
}

// unexport drops an `export` prefix, which the keyword's own separator ends:
// any whitespace, not one space. The file is shared with tools that source it,
// and dropping the line would leave a value visible in the file and missing
// from the run — the outcome a tab used to produce.
func unexport(name string) string {
	rest, found := strings.CutPrefix(name, "export")
	if !found || rest == strings.TrimLeftFunc(rest, space) {
		return name
	}
	return rest
}

func declarable(key string) bool {
	if !keyPattern.MatchString(key) {
		return false
	}
	for _, prefix := range reservedPrefixes {
		if strings.HasPrefix(key, prefix) {
			return false
		}
	}
	return true
}

// value reproduces dotenv's LINE regex value alternation plus its post-match
// trim, quote-strip, and escape expansion: a leading quoted token wins whole
// when its close is followed by nothing but whitespace and an optional
// comment; otherwise the value runs up to the first '#'. Only a double-quoted
// value expands escapes, and only `\n` and `\r` — dotenv has no `#` escape.
func value(rest string) string {
	v := quotedToken(rest)
	if v == "" {
		v, _, _ = strings.Cut(rest, "#")
	}
	v = trimSpace(v)
	if v == "" {
		return v
	}
	quote := v[0]
	if len(v) >= 2 && (quote == '\'' || quote == '"' || quote == '`') && v[len(v)-1] == quote {
		v = v[1 : len(v)-1]
	}
	if quote == '"' {
		v = strings.ReplaceAll(v, `\n`, "\n")
		v = strings.ReplaceAll(v, `\r`, "\r")
	}
	return v
}

// quotedToken returns rest's leading quoted token — starting with a single,
// double, or backtick quote — iff a close for it exists whose tail is nothing
// but whitespace and an optional comment. It returns "" for no leading quote
// or no such close, so the caller falls back to the '#'-cut branch. The scan
// mirrors dotenv's own alternation: a greedy match stops at the first
// unescaped quote, then backtracks to an earlier one if the tail after it
// fails the check.
func quotedToken(rest string) string {
	t := strings.TrimLeftFunc(rest, space)
	if t == "" {
		return ""
	}
	quote := t[0]
	if quote != '\'' && quote != '"' && quote != '`' {
		return ""
	}
	var closes []int
	for i := 1; i < len(t); i++ {
		if t[i] != quote {
			continue
		}
		closes = append(closes, i)
		if t[i-1] != '\\' {
			break
		}
	}
	for i := len(closes) - 1; i >= 0; i-- {
		tail := strings.TrimLeftFunc(t[closes[i]+1:], space)
		if tail == "" || tail[0] == '#' {
			return t[:closes[i]+1]
		}
	}
	return ""
}

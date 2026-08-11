package dotenv

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const FileName = ".env"

var keyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

var reservedPrefixes = []string{"OCEL_"}

type File struct {
	Values     map[string]string
	Unreadable []int
}

func Load(dir string) (File, error) {
	file, err := os.Open(filepath.Join(dir, FileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
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
		return 0, nil, nil
	case end+1 < len(data) && data[end+1] == '\n':
		return end + 2, data[:end], nil
	default:
		return end + 1, data[:end], nil
	}
}

func space(r rune) bool { return (unicode.IsSpace(r) && r != '\u0085') || r == '\ufeff' }

func trimSpace(s string) string { return strings.TrimFunc(s, space) }

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

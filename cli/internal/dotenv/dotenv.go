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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FileName is the dotfile, at the project root and nowhere else. Dev elects one
// leader per project root, so a file found relative to a working directory
// would hand a leader and a follower of the same project different values.
const FileName = ".env"

// keyPattern and reservedPrefixes are the SDK's own rule for a key defineEnv
// could name (packages/ocel/src/env/definition.ts). A line outside it belongs
// to whatever else reads this file.
var keyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

var reservedPrefixes = []string{"OCEL_", "AWS_", "LAMBDA_", "NEXT_PUBLIC_"}

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

// parseLine reports false for a line that assigns nothing, and an empty key for
// one that assigns something Ocel could not be asked for. A '#' outside the
// first column is part of the value: a trailing comment cannot be told from one
// inside a value without quoting rules deeper than this grammar has.
func parseLine(raw string) (string, string, bool) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", true
	}

	name, rest, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}

	// `export KEY=VALUE` is read the same way: the file is shared with tools
	// that source it, and dropping the line would leave a value visible in the
	// file and missing from the run.
	key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "export "))
	if !declarable(key) {
		return "", "", true
	}
	return key, unquote(strings.TrimSpace(rest)), true
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

// unquote strips one matching pair of surrounding quotes. Only a double-quoted
// value has escapes, and only `\n`.
func unquote(value string) string {
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1]
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return strings.ReplaceAll(value[1:len(value)-1], `\n`, "\n")
	}
	return value
}

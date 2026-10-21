package sourcearchive

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

const npmrcName = ".npmrc"

var credentialPatterns = parsePatterns(
	"*.pem",
	"*.key",
	"*.p12",
	"*.pfx",
	"id_rsa*",
	"id_dsa*",
	"id_ecdsa*",
	"id_ed25519*",
	".netrc",
	"_netrc",
	".git-credentials",
	".pgpass",
	".pypirc",
	".aws/",
	".ssh/",
	".gnupg/",
	".kube/",
	"**/.docker/config.json",
)

var npmrcCredentialKeys = []string{"_auth", "_authToken", "_password"}

func parsePatterns(lines ...string) []gitignore.Pattern {
	patterns := make([]gitignore.Pattern, 0, len(lines))
	for _, line := range lines {
		patterns = append(patterns, gitignore.ParsePattern(line, nil))
	}
	return patterns
}

func isSecret(name, file string) (bool, error) {
	switch {
	case strings.HasPrefix(name, envFilePrefix):
		return true, nil
	case name == npmrcName:
		return holdsRegistryCredential(file)
	}
	return false, nil
}

func holdsRegistryCredential(npmrc string) (bool, error) {
	opened, err := os.Open(npmrc)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", npmrc, err)
	}
	defer func() { _ = opened.Close() }()
	lines := bufio.NewScanner(opened)
	for lines.Scan() {
		key, value, ok := strings.Cut(lines.Text(), "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || value == "" || strings.Contains(value, "${") {
			continue
		}
		if slices.Contains(npmrcCredentialKeys, key[strings.LastIndex(key, ":")+1:]) {
			return true, nil
		}
	}
	if err := lines.Err(); err != nil {
		return false, fmt.Errorf("read %s: %w", npmrc, err)
	}
	return false, nil
}

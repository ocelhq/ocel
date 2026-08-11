package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const maxSafeNamePrefixLen = 40

func StateBackendURL(bucket, slug string) string {
	return "s3://" + bucket + "/" + safeName(slug)
}

func safeName(logicalName string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(logicalName) {
		safe := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !safe {
			r = '-'
		}
		if r == '-' {
			if prevDash || b.Len() == 0 {
				continue
			}
			prevDash = true
			b.WriteRune('-')
			continue
		}
		prevDash = false
		b.WriteRune(r)
	}
	name := strings.TrimRight(b.String(), "-")

	if name == "" {
		name = "a"
	}
	if first := name[0]; first < 'a' || first > 'z' {
		name = "a" + name
	}
	if len(name) > maxSafeNamePrefixLen {
		sum := sha256.Sum256([]byte(logicalName))
		suffix := "-" + hex.EncodeToString(sum[:])[:8]
		name = strings.TrimRight(name[:maxSafeNamePrefixLen-len(suffix)], "-") + suffix
	}
	return name
}

const (
	maxLambdaNameLen        = 64
	lambdaAutonameSuffixLen = 8
)

func lambdaResourceName(logicalName string) string {
	max := maxLambdaNameLen - lambdaAutonameSuffixLen
	if len(logicalName) <= max {
		return logicalName
	}
	sum := sha256.Sum256([]byte(logicalName))
	suffix := "_" + hex.EncodeToString(sum[:])[:8]
	return logicalName[:max-len(suffix)] + suffix
}

func physicalNamePrefix(logicalName, infix string) string {
	prefix := safeName(logicalName) + "-"
	if infix != "" {
		prefix += infix + "-"
	}
	return prefix
}

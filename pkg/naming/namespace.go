package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

const AppWord = "app"

const (
	MaxNamespaceField    = 12
	namespaceDigestChars = 6
	namespaceHeadChars   = MaxNamespaceField - len(WordSeparator) - namespaceDigestChars
)

var compactedPattern = regexp.MustCompile(`^[a-z0-9-]{1,5}-[0-9a-f]{6}$`)

func NamespaceField(namespace string) string {
	if len(namespace) <= MaxNamespaceField {
		return namespace
	}
	head := strings.TrimRight(namespace[:namespaceHeadChars], WordSeparator)
	if strings.HasSuffix(WordSeparator+head, WordSeparator+AppWord) {
		head = strings.TrimRight(head[:len(head)-1], WordSeparator)
	}
	sum := sha256.Sum256([]byte(namespace))
	return head + WordSeparator + hex.EncodeToString(sum[:])[:namespaceDigestChars]
}

func IsCompactedNamespaceField(value string) bool {
	return compactedPattern.MatchString(value)
}

func AppFence(namespace string) string {
	return NamespaceField(namespace) + WordSeparator + AppWord + WordSeparator
}

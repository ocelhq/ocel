package naming

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

const (
	tagChars        = 6
	randomAlphabet  = "0123456789abcdefghijklmnopqrstuvwxyz"
	unbiasedByteCap = 256 / len(randomAlphabet) * len(randomAlphabet)
)

var randomTagPattern = regexp.MustCompile(`^[0-9a-z]{6}$`)

func NewRandomTag() string {
	tag := make([]byte, 0, tagChars)
	buf := make([]byte, tagChars)
	for len(tag) < tagChars {
		rand.Read(buf)
		for _, b := range buf {
			if int(b) < unbiasedByteCap && len(tag) < tagChars {
				tag = append(tag, randomAlphabet[int(b)%len(randomAlphabet)])
			}
		}
	}
	return string(tag)
}

func IsRandomTag(value string) bool {
	return randomTagPattern.MatchString(value)
}

func HashTag(fields ...string) string {
	return HashDigits(tagChars, fields...)
}

func HashDigits(digits int, fields ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(fields, FieldSeparator)))
	return hex.EncodeToString(sum[:])[:digits]
}

func (c Coordinate) HashTag(namespace string) string {
	return HashTag(namespace, c.Project, c.Env, appField(c.App), string(c.Kind), c.Name)
}

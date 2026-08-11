package deploy

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/ocelhq/ocel/pkg/naming"
)

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

const maxPhysicalNamePrefixLen = 40

func physicalNamePrefix(logicalName, infix string) string {
	prefix := naming.Fit(maxPhysicalNamePrefixLen, naming.WordSeparator, naming.Compressible(naming.SanitizeAlpha(logicalName))) + naming.WordSeparator
	if infix != "" {
		prefix += infix + naming.WordSeparator
	}
	return prefix
}

package naming

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
)

const (
	SpanEnvironment = "environment"
	SpanEdge        = "edge"
	SpanHostnames   = "hostnames"
	SpanPromotion   = "promotion"
	SpanConnector   = "connector"

	SpanIDLen = 8
)

func SpanID(name string) []byte {
	h := sha256.New()
	writeSpanField(h, name)
	return h.Sum(nil)[:SpanIDLen]
}

func writeSpanField(h hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	h.Write(size[:])
	h.Write([]byte(value))
}

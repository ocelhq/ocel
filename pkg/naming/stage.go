package naming

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
)

const (
	UnitEnvironment = "environment"
	UnitEdge        = "edge"
	UnitHostnames   = "hostnames"
	UnitPromotion   = "promotion"
	UnitConnector   = "connector"

	StageIDLen = 8
)

func UnitID(unit string) []byte {
	h := sha256.New()
	writeStageField(h, unit)
	return h.Sum(nil)[:StageIDLen]
}

func writeStageField(h hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	h.Write(size[:])
	h.Write([]byte(value))
}

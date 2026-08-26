package naming

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
)

const (
	UnitEnvironment = "environment"
	UnitEdge        = "edge"
	UnitPromotion   = "promotion"

	PhaseBuilding     = "building"
	PhaseUploading    = "uploading"
	PhaseProvisioning = "provisioning"
	PhaseFinalizing   = "finalizing"
	PhaseDeleting     = "deleting"

	StageIDLen = 8
)

func UnitID(unit string) []byte {
	h := sha256.New()
	writeStageField(h, unit)
	return h.Sum(nil)[:StageIDLen]
}

func PhaseID(unit, phase string) []byte {
	h := sha256.New()
	writeStageField(h, unit)
	writeStageField(h, phase)
	return h.Sum(nil)[:StageIDLen]
}

func writeStageField(h hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	h.Write(size[:])
	h.Write([]byte(value))
}

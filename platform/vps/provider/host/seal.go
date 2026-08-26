package host

import (
	_ "embed"
	"io/fs"
)

//go:embed seal.py
var sealScript []byte

const SealAlgorithm = "aes-256-gcm"

const (
	sealKeyBytes             = 32
	sealKeyMode  fs.FileMode = 0o400
)

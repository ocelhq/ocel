package images

import (
	"cmp"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/kvstore"
)

var valkeyImages = map[string]string{
	"8": "valkey/valkey:8.1.10@sha256:640c5e62cea04b6d6f2084232651d0cc70362d31f4f805e7be94dbed6855e8f2",
	"9": "valkey/valkey:9.1.2@sha256:418652cfb58ef879d4978c33553735d7147016032d5aefaa14c828e611eb9dfd",
}

func Valkey(version string) (string, bool) {
	image, pinned := valkeyImages[cmp.Or(version, kvstore.DefaultVersion)]
	return image, pinned
}

func ValkeyVersions() []string {
	return slices.Sorted(maps.Keys(valkeyImages))
}

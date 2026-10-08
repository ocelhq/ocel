package image

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
)

const (
	buildLiveDir = "/run/ocel-live"

	liveDirEnv = "OCEL_LIVE_DIR"

	buildArgAttrPrefix = "build-arg:"

	railpackSecretsHashArg = "secrets-hash"

	dockerfileLiveHashArg = "OCEL_LIVE_HASH"

	networkHostEntitlement = "network.host"

	secretFileMode = 0o400
)

type LiveValues struct {
	Values map[string]string
	Env    map[string]string
	Hash   string
}

func NewLiveValues(values, env map[string]string, hashKey []byte) LiveValues {
	if len(values) == 0 && len(env) == 0 {
		return LiveValues{}
	}
	mac := hmac.New(sha256.New, hashKey)
	writeHashed(mac, values)
	fmt.Fprint(mac, "env:")
	writeHashed(mac, env)
	return LiveValues{Values: maps.Clone(values), Env: maps.Clone(env), Hash: hex.EncodeToString(mac.Sum(nil))}
}

func writeHashed(mac io.Writer, values map[string]string) {
	for _, key := range slices.Sorted(maps.Keys(values)) {
		fmt.Fprintf(mac, "%d:%s%d:%s", len(key), key, len(values[key]), values[key])
	}
}

func (l LiveValues) hasValues() bool { return len(l.Values) > 0 || len(l.Env) > 0 }

func (l LiveValues) listKeys() []string { return slices.Sorted(maps.Keys(l.Values)) }

func (l LiveValues) listEnvKeys() []string { return slices.Sorted(maps.Keys(l.Env)) }

func (l LiveValues) newSecretsSession() []session.Attachable {
	if !l.hasValues() {
		return nil
	}
	values := make(map[string][]byte, len(l.Values)+len(l.Env))
	for key, value := range l.Values {
		values[key] = []byte(value)
	}
	for key, value := range l.Env {
		values[key] = []byte(value)
	}
	return []session.Attachable{secretsprovider.FromMap(values)}
}

// Package live is the live-delivery format: the single definition of what a
// deploy pins into a function's package so the membrane can fetch that
// function's live-class values at runtime, and nothing more.
//
// A live value is never in an artifact. What rides in the package is its
// address — the store to read, and the coordinate of each key — so possession
// of the code discloses where a value lives and not what it is. Rotating a
// value changes nothing here, which is exactly why rotation needs no redeploy;
// re-scoping a key does change it, and lands as a new artifact because the file
// is folded into the package's content hash.
//
// The manifest is a file rather than function-configuration entries so it does
// not compete for the platform's 4KB environment budget, which a handful of
// coordinates would otherwise consume.
//
// It carries no cloud dependency: the deploy writes it and the runtime
// bootstrap reads it, and neither side needs the other's clients to agree on
// the shape.
package live

import (
	"encoding/json"
	"fmt"
)

// FilePath is where an app's live-value manifest rides inside every one of its
// function deployment packages, relative to the task root. It sits beside the
// encrypted-baked bundle, under the same reserved directory.
const FilePath = ".ocel/variables.live.json"

// Manifest is one app's live-value addresses. Table, KeyARN and Class are the
// substrate's, resolved at deploy from the account's bootstrap rather than
// discovered at runtime: resolving them in the sandbox would mean linking a
// CloudFormation client into the cold path of every cold start.
type Manifest struct {
	Slug   string `json:"slug"`
	Table  string `json:"table"`
	KeyARN string `json:"keyArn"`
	Class  string `json:"class"`
	Keys   []Key  `json:"keys"`
}

// Key is one live variable's coordinate, less the components the manifest
// already states once. Folder is where resolution decided the key reads from,
// which is not the app's binding — an unscoped key resolves at the project root
// even for a bound app. Empty is the project root, the same single spelling
// used everywhere above the store; the store owns the sentinel it renders that
// as, and nothing here may spell it.
//
// There is no environment component: every live value binds class-wide today,
// and a field nothing populates would only invite the sentinel to be written
// out literally, which the store rejects.
type Key struct {
	Key    string `json:"key"`
	Folder string `json:"folder,omitempty"`
}

// Render encodes a manifest for the bundle. It refuses a manifest that names
// keys without saying where to read them: a function that came up with half an
// address would fail at its first fetch, in the sandbox, with nothing to point
// at. The deploy is where that is diagnosable.
func Render(m Manifest) ([]byte, error) {
	if len(m.Keys) == 0 {
		return nil, nil
	}
	// Ordered, because a manifest missing two components must name the same one
	// every time: an error that says "no variable table" on one run and "no
	// environment class" on the next for identical input is one nobody can act
	// on twice.
	for _, component := range []struct{ name, value string }{
		{"project slug", m.Slug},
		{"variable table", m.Table},
		{"variable key", m.KeyARN},
		{"environment class", m.Class},
	} {
		if component.value == "" {
			return nil, fmt.Errorf("the live-value manifest names %d keys but no %s", len(m.Keys), component.name)
		}
	}
	return json.Marshal(m)
}

// Parse reads a manifest back. Every failure is returned rather than absorbed,
// for the same reason a bundle that will not open is fatal: a variable silently
// unset is read at the point of use as one that was never required.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", FilePath, err)
	}
	return m, nil
}

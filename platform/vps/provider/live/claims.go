package live

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	ProxyDir     = StateRoot + "/proxy"
	ProxyConfig  = ProxyDir + "/caddy.json"
	RoutingDir   = StateRoot + "/routing"
	RoutingTable = RoutingDir + "/table.json"
)

const (
	ClaimPrefix    = "ocel-host-"
	ClaimSeparator = "/"
)

const StoreLabel = "storage"

func Surface(slug, class string) string {
	return naming.Join(naming.FieldSeparator, "ocel", naming.Sanitize(slug), class)
}

func StoreHostname(hostname string) string {
	return StoreLabel + "." + hostname
}

type Claimed struct {
	Owner    string `json:"owner"`
	Hostname string `json:"hostname"`
	Pointer  string `json:"pointer"`
	App      string `json:"app,omitempty"`
}

func ClaimIdentity(c Claimed) string {
	fields := []string{c.Owner, c.Hostname, c.Pointer}
	if c.App != "" {
		fields = append(fields, c.App)
	}
	return ClaimPrefix + strings.Join(fields, ClaimSeparator)
}

func ClaimedIn(table []byte) ([]Claimed, error) {
	var read struct {
		Claims []Claimed `json:"claims"`
	}
	if err := json.Unmarshal(table, &read); err != nil {
		return nil, fmt.Errorf("read what this box claims: %w", err)
	}
	return read.Claims, nil
}

func StoreBase(claims []Claimed, owner, pointer string) string {
	hostnames := make([]string, 0, len(claims))
	for _, claim := range claims {
		if claim.Owner == owner && claim.Pointer == pointer && claim.App == StoreLabel {
			hostnames = append(hostnames, claim.Hostname)
		}
	}
	if len(hostnames) == 0 {
		return ""
	}
	slices.Sort(hostnames)
	return "https://" + hostnames[0]
}

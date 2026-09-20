package live

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	ProxyDir    = StateRoot + "/proxy"
	ProxyConfig = ProxyDir + "/caddy.json"
)

const (
	ProxyServer    = "ocel"
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
	Owner    string
	Hostname string
	Pointer  string
	App      string
}

func ClaimIdentity(c Claimed) string {
	fields := []string{c.Owner, c.Hostname, c.Pointer}
	if c.App != "" {
		fields = append(fields, c.App)
	}
	return ClaimPrefix + strings.Join(fields, ClaimSeparator)
}

func ClaimedAt(identity string) (Claimed, bool) {
	named, mine := strings.CutPrefix(identity, ClaimPrefix)
	if !mine {
		return Claimed{}, false
	}
	fields := strings.Split(named, ClaimSeparator)
	if len(fields) < 3 || len(fields) > 4 || slices.Contains(fields, "") {
		return Claimed{}, false
	}
	held := Claimed{Owner: fields[0], Hostname: fields[1], Pointer: fields[2]}
	if len(fields) == 4 {
		held.App = fields[3]
	}
	return held, true
}

type proxyDocument struct {
	Apps struct {
		HTTP struct {
			Servers map[string]struct {
				Routes []struct {
					Identity string `json:"@id"`
				} `json:"routes"`
			} `json:"servers"`
		} `json:"http"`
	} `json:"apps"`
}

func ClaimedIn(document []byte) ([]Claimed, error) {
	var read proxyDocument
	if err := json.Unmarshal(document, &read); err != nil {
		return nil, fmt.Errorf("read what this box claims: %w", err)
	}
	var claims []Claimed
	for _, route := range read.Apps.HTTP.Servers[ProxyServer].Routes {
		if held, mine := ClaimedAt(route.Identity); mine {
			claims = append(claims, held)
		}
	}
	return claims, nil
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

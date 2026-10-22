package cloudflare

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func refuseOtherOrigin(found, hostname string, held OriginScope) error {
	return refusal.Refuse(refusal.CodeInvalid, "%s", describeOtherOrigin(found, hostname, held))
}

func describeOtherOrigin(found, hostname string, held OriginScope) string {
	subject := "hostname"
	if strings.HasPrefix(hostname, "*.") {
		subject = "preview base domain"
	}
	return fmt.Sprintf("%s, and one %s belongs to one origin: release %s on %s first", found, subject, hostname, held)
}

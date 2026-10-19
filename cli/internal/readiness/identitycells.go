package readiness

import (
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func IdentityCells(identity *contractv1.Identity) []string {
	var account []string
	for _, value := range []string{identity.GetProvider(), identity.GetAccount(), identity.GetLocation()} {
		if value != "" {
			account = append(account, value)
		}
	}
	var cells []string
	if len(account) > 0 {
		cells = append(cells, strings.Join(account, " "))
	}
	for _, detail := range identity.GetDetails() {
		if detail.GetValue() == "" {
			continue
		}
		cells = append(cells, strings.TrimSpace(detail.GetLabel()+" "+detail.GetValue()))
	}
	return cells
}

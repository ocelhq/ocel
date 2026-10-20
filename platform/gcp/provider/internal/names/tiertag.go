package names

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	TierTagKey   = "ocel-tier"
	OutsideValue = "outside"
)

func (n Names) TierTagKeyPath() string { return n.project + "/" + TierTagKey }

func (n Names) TierTagValuePath(tier environment.Tier) string {
	return n.TierTagKeyPath() + "/" + string(tier)
}

func (n Names) OutsideTagValuePath() string { return n.TierTagKeyPath() + "/" + OutsideValue }

var ocelAccount = regexp.MustCompile("^[a-z][a-z0-9-]{0," + strconv.Itoa(naming.MaxNamespaceField-1) + "}-(" +
	naming.AppWord + "-[0-9a-f]{" + strconv.Itoa(AccountHashDigits) + "}|(" +
	naming.TierWord(environment.TierProduction) + "|" + naming.TierWord(environment.TierPreview) + ")-(" +
	strings.Join([]string{realtimeInfix, assetReaderInfix, envSourceSyncName, bastionName, pushAccountName}, "|") + "))$")

const accountMark = "; made by ocel"

func MarkedAccountDescription(description string) string { return description + accountMark }

func UnmarkedAccountDescription(description string) string {
	unmarked, _ := strings.CutSuffix(description, accountMark)
	return unmarked
}

func IsOcelAccount(id, description string) bool {
	return ocelAccount.MatchString(id) && strings.HasSuffix(description, accountMark)
}

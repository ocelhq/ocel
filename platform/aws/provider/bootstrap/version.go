package bootstrap

import "fmt"

const RequiredBootstrapVersion = 12

type Compatibility int

const (
	Compatible Compatibility = iota
	NeedsBootstrapInit
	NeedsBootstrapUpgrade
	NeedsCLIUpgrade
)

func CheckCompat(deployed int, present bool, required int) Compatibility {
	switch {
	case !present:
		return NeedsBootstrapInit
	case deployed < required:
		return NeedsBootstrapUpgrade
	case deployed > required:
		return NeedsCLIUpgrade
	default:
		return Compatible
	}
}

func (c Compatibility) Explain(deployed, required int, cmd string) error {
	switch c {
	case NeedsBootstrapInit:
		return fmt.Errorf("this AWS account has no Ocel bootstrap.\nRun `%s` to create it, then try again", cmd)
	case NeedsBootstrapUpgrade:
		if deployed == 0 {
			return fmt.Errorf("this AWS account's Ocel bootstrap predates version tracking; this provider requires version %d.\nRun `%s` to upgrade it, then try again", required, cmd)
		}
		return fmt.Errorf("this AWS account's Ocel bootstrap is out of date: the account is at version %d, this provider requires version %d.\nRun `%s` to upgrade it, then try again", deployed, required, cmd)
	case NeedsCLIUpgrade:
		return fmt.Errorf("this AWS account's Ocel bootstrap is newer than this provider understands: the account is at version %d, this provider supports up to version %d.\nUpgrade the Ocel CLI and try again", deployed, required)
	default:
		return nil
	}
}

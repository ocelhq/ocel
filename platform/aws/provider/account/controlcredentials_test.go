package account_test

import (
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
)

func init() {
	account.CredentialPolicies = control.CredentialPolicies
}

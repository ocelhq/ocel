package vps_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/constants"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func TestAnAppReachesTheStoreUnderAnAccountOfItsOwn(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	manifest := storeManifest(t, machine, vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}})

	own := host.StoreAccountKey(manifest.Store.Env, "web")
	if manifest.Store.AccessKeyID != own {
		t.Errorf("the app reaches the store as %q, want the account %q this app alone holds: the root credential reaches every bucket on the box",
			manifest.Store.AccessKeyID, own)
	}

	granted := strings.Join(machine.commands(), "\n")
	if !strings.Contains(granted, "add-service-account") || !strings.Contains(granted, "update-service-account") {
		t.Fatalf("the store was never asked for an account of the app's own:\n%s", granted)
	}
}

func fedBy(t *testing.T, machine *box, needle string) string {
	t.Helper()
	encoded := machine.fedTo(needle)
	if encoded == "" {
		t.Fatalf("nothing was fed to the store by the call that would %s", needle)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("what is fed to the store is not what the box decodes: %v", err)
	}
	return string(decoded)
}

func TestAnAppsStoreAccountIsHeldToTheBucketsItBinds(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	storeManifest(t, machine, vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}})

	policy := fedBy(t, machine, "add-service-account")
	for what, named := range map[string]string{
		"the bucket the app binds":      "prod-web-r0a1b2c3d-uploads",
		"the store's own sessions":      constants.StoreSessionsBucket(),
		"a policy naming what it holds": "arn:aws:s3:::",
	} {
		if !strings.Contains(policy, named) {
			t.Errorf("the account is granted without %s:\n%s", what, policy)
		}
	}
	if strings.Contains(policy, `"arn:aws:s3:::*"`) {
		t.Error("the app's account is granted every bucket on the store, which is the root credential by another name")
	}
}

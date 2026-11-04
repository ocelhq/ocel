package gcp

import (
	"testing"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
)

func TestAKeyWhosePrimaryVersionIsGoneIsSurveyedAsOneTheBootstrapReplaces(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		key      *kmspb.CryptoKey
		present  bool
		replaces bool
	}{
		"no key":                          {},
		"a key sealing under its primary": {key: &kmspb.CryptoKey{Primary: &kmspb.CryptoKeyVersion{State: kmspb.CryptoKeyVersion_ENABLED}}, present: true},
		"a key whose primary is scheduled for destruction": {
			key: &kmspb.CryptoKey{Primary: &kmspb.CryptoKeyVersion{State: kmspb.CryptoKeyVersion_DESTROY_SCHEDULED}}, replaces: true,
		},
		"a key whose primary is destroyed": {
			key: &kmspb.CryptoKey{Primary: &kmspb.CryptoKeyVersion{State: kmspb.CryptoKeyVersion_DESTROYED}}, replaces: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			found := bootstrap.KeyPresenceOf(tc.key)
			if found.Present != tc.present || (found.Replaces != "") != tc.replaces {
				t.Errorf("keyPresenceOf() = %+v, want present=%t replacing=%t: a new primary version leaves what the tier sealed under the old one unopenable", found, tc.present, tc.replaces)
			}
		})
	}
}

func TestASecretWhosePassphraseIsNoLongerEnabledIsSurveyedAsOneTheBootstrapReplaces(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		latest   *secretmanager.SecretVersion
		present  bool
		replaces bool
	}{
		"a secret holding no passphrase yet":     {},
		"a secret holding its passphrase":        {latest: &secretmanager.SecretVersion{State: "ENABLED"}, present: true},
		"a secret whose passphrase is disabled":  {latest: &secretmanager.SecretVersion{State: "DISABLED"}, replaces: true},
		"a secret whose passphrase is destroyed": {latest: &secretmanager.SecretVersion{State: "DESTROYED"}, replaces: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			found := bootstrap.PassphrasePresenceOf(tc.latest)
			if found.Present != tc.present || (found.Replaces != "") != tc.replaces {
				t.Errorf("passphrasePresenceOf() = %+v, want present=%t replacing=%t: a new passphrase is not the one what the tier holds was written under", found, tc.present, tc.replaces)
			}
		})
	}
}

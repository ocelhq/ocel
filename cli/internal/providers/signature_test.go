package providers

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

func verifierFor(t *testing.T, material root.TrustedMaterial) *verify.Verifier {
	t.Helper()

	verifier, err := verify.NewVerifier(material, verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return verifier
}

func TestTheSigstoreTrustedRootThisCLIWasBuiltWithVerifiesARealFulcioBundle(t *testing.T) {
	t.Parallel()

	material, err := root.NewTrustedRootFromJSON(trustedRoot)
	if err != nil {
		t.Fatalf("NewTrustedRootFromJSON: %v", err)
	}
	verifier, err := publicGood(material)
	if err != nil {
		t.Fatalf("publicGood: %v", err)
	}

	raw, err := os.ReadFile("testdata/fulcio-signed.sigstore.json")
	if err != nil {
		t.Fatalf("read the bundle: %v", err)
	}
	var signed bundle.Bundle
	if err := json.Unmarshal(raw, &signed); err != nil {
		t.Fatalf("read the bundle as a sigstore bundle: %v", err)
	}

	policy := verify.NewPolicy(verify.WithoutArtifactUnsafe(), verify.WithoutIdentitiesUnsafe())
	if _, err := verifier.Verify(&signed, policy); err != nil {
		t.Fatalf("the embedded trusted root does not verify a bundle the public-good instance issued: %v", err)
	}
}

func TestChecksumsSignedByTheReleaseWorkflowVerify(t *testing.T) {
	t.Parallel()

	sigstore, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore: %v", err)
	}
	checksums := []byte("0000  ocel-provider-aws_0.2.0_linux_amd64.tar.gz\n")
	identity := SignerIdentity("0.2.0")

	signed, err := sigstore.Sign(identity, SignerIssuer, checksums)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := verifySigned(verifierFor(t, sigstore), signed, checksums, identity); err != nil {
		t.Fatalf("verifySigned: %v", err)
	}
}

func TestChecksumsAlteredAfterTheyWereSignedAreRefused(t *testing.T) {
	t.Parallel()

	sigstore, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore: %v", err)
	}
	checksums := []byte("0000  ocel-provider-aws_0.2.0_linux_amd64.tar.gz\n")
	identity := SignerIdentity("0.2.0")

	signed, err := sigstore.Sign(identity, SignerIssuer, checksums)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	altered := []byte("1111  ocel-provider-aws_0.2.0_linux_amd64.tar.gz\n")
	if err := verifySigned(verifierFor(t, sigstore), signed, altered, identity); err == nil {
		t.Fatal("verifySigned() error = nil, want checksums altered after signing refused")
	}
}

func TestASignatureMadeUnderAnotherIdentityIsRefusedNamingBoth(t *testing.T) {
	t.Parallel()

	sigstore, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore: %v", err)
	}
	checksums := []byte("0000  ocel-provider-aws_0.2.0_linux_amd64.tar.gz\n")
	elsewhere := "https://github.com/elsewhere/ocel/.github/workflows/binaries.yml@refs/tags/v0.2.0"

	signed, err := sigstore.Sign(elsewhere, SignerIssuer, checksums)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	err = verifySigned(verifierFor(t, sigstore), signed, checksums, SignerIdentity("0.2.0"))
	if err == nil {
		t.Fatal("verifySigned() error = nil, want a signature made under another workflow refused")
	}
	for _, want := range []string{SignerIdentity("0.2.0"), elsewhere} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

func TestASignatureMadeUnderAnotherTagIsRefused(t *testing.T) {
	t.Parallel()

	sigstore, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore: %v", err)
	}
	checksums := []byte("0000  ocel-provider-aws_0.2.0_linux_amd64.tar.gz\n")

	signed, err := sigstore.Sign(SignerIdentity("0.3.0"), SignerIssuer, checksums)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := verifySigned(verifierFor(t, sigstore), signed, checksums, SignerIdentity("0.2.0")); err == nil {
		t.Fatal("verifySigned() error = nil, want a signature made over another release's tag refused")
	}
}

func TestASignatureMadeUnderAnotherIssuerIsRefused(t *testing.T) {
	t.Parallel()

	sigstore, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore: %v", err)
	}
	checksums := []byte("0000  ocel-provider-aws_0.2.0_linux_amd64.tar.gz\n")
	identity := SignerIdentity("0.2.0")

	signed, err := sigstore.Sign(identity, "https://accounts.google.com", checksums)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := verifySigned(verifierFor(t, sigstore), signed, checksums, identity); err == nil {
		t.Fatal("verifySigned() error = nil, want a signature made under another issuer refused")
	}
}

func TestABundleThatIsNotOneIsRefusedBeforeAnythingIsPinned(t *testing.T) {
	t.Parallel()

	err := VerifyChecksums([]byte("0000  x\n"), []byte("not a bundle"), SignerIdentity("0.2.0"))
	if err == nil {
		t.Fatal("VerifyChecksums() error = nil, want a signature that is not a bundle refused")
	}
	if !strings.Contains(err.Error(), SignatureAsset) {
		t.Errorf("error %q does not name what it failed to read", err.Error())
	}
}

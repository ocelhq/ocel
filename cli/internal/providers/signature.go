package providers

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const SignatureAsset = ChecksumsAsset + ".sigstore.json"

const (
	SignerIssuer   = "https://token.actions.githubusercontent.com"
	SignerWorkflow = "https://github.com/ocelhq/ocel/.github/workflows/binaries.yml"
)

//go:embed trustedroot.json
var trustedRoot []byte

type ChecksumVerifier func(checksums, signature []byte, identity string) error

func SignerIdentity(version string) string {
	return SignerWorkflow + "@refs/tags/v" + version
}

func VerifyChecksums(checksums, signature []byte, identity string) error {
	material, err := root.NewTrustedRootFromJSON(trustedRoot)
	if err != nil {
		return fmt.Errorf("read the sigstore trusted root this CLI was built with: %w", err)
	}
	verifier, err := publicGood(material)
	if err != nil {
		return err
	}
	var signed bundle.Bundle
	if err := json.Unmarshal(signature, &signed); err != nil {
		return fmt.Errorf("read %s as a sigstore bundle: %w", SignatureAsset, err)
	}
	return verifySigned(verifier, &signed, checksums, identity)
}

func publicGood(material root.TrustedMaterial) (*verify.Verifier, error) {
	verifier, err := verify.NewVerifier(material,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return nil, fmt.Errorf("verify %s: %w", SignatureAsset, err)
	}
	return verifier, nil
}

func verifySigned(verifier *verify.Verifier, signed verify.SignedEntity, checksums []byte, identity string) error {
	who, err := verify.NewShortCertificateIdentity(SignerIssuer, "", identity, "")
	if err != nil {
		return fmt.Errorf("verify %s: %w", SignatureAsset, err)
	}
	policy := verify.NewPolicy(verify.WithArtifact(bytes.NewReader(checksums)), verify.WithCertificateIdentity(who))
	if _, err := verifier.Verify(signed, policy); err != nil {
		return fmt.Errorf("%s carries no signature over %s made by %s as %s: %w", SignatureAsset, ChecksumsAsset, SignerIssuer, identity, err)
	}
	return nil
}

package conformance

import (
	"context"
	"path/filepath"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func takesTheWebIdentityItIsHanded(t *testing.T, suite Suite) {
	t.Helper()

	facts := construct(t, suite, suite.Server.New).Facts()
	if facts.WebIdentity == nil {
		return
	}
	for _, name := range facts.WebIdentity.CredentialEnvVars {
		t.Setenv(name, "")
	}
	child := spawn(t, suite.Binary)
	req := configureWith(t, suite.Options)
	req.Config.WebIdentity = &contractv1.WebIdentity{
		TokenFile:        filepath.Join(t.TempDir(), "token"),
		IdentityProvider: "conformance-identity-provider",
		Principal:        "conformance-principal",
		SessionName:      "conformance",
	}
	if _, err := client(child.http, providerURL).Configure(context.Background(), req); err != nil {
		t.Fatalf("Configure() with a web identity, in a run that names no credential of its own = %v, want the web identity taken", err)
	}
}

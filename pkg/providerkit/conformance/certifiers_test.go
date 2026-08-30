package conformance_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

const statesNoHostname = "OCEL_CERTIFIER_CHECKS_NAME_NO_HOSTNAME"

func TestACertifierSuiteNamingNoHostnameSkipsItsLoopsRatherThanReportingThemPassed(t *testing.T) {
	if os.Getenv(statesNoHostname) == "1" {
		conformance.RunCertifier(t, fake.NewProvider(fake.Options{Region: "nowhere"}),
			conformance.CertifierChecks{Kind: fake.KindRelay})
		return
	}

	inner := exec.Command(os.Args[0], "-test.run", "^"+t.Name()+"$", "-test.v")
	inner.Env = append(os.Environ(), statesNoHostname+"=1")
	rendered, err := inner.CombinedOutput()
	if err != nil {
		t.Fatalf("the certifier tier over a suite naming no hostname = %v\n%s", err, rendered)
	}
	for _, named := range []string{
		"a_held_handle_names_what_it_terminates_and_who_renews_it",
		"a_certificate_ocel_never_requested_is_never_ocel's_to_discard",
		"the_handle_is_the_vocabulary_this_provider_mints",
	} {
		want := "--- SKIP: " + t.Name() + "/" + named
		if !strings.Contains(string(rendered), want) {
			t.Errorf("%s carries no %q, and a loop over no hostname reports a pass having held this provider to nothing\n%s",
				named, want, rendered)
		}
	}
}

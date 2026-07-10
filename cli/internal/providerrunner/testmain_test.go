package providerrunner

import (
	"os"
	"testing"
)

// fakeProviderEnvVar, when set to "1" in a re-exec of this test binary,
// tells TestMain to run as a fake provider process instead of the real test
// suite (Go's helper-process pattern, as used by os/exec's own tests). This
// is how tests exercise Spawn/Ready/Deploy/Close against a real child
// process without requiring the real provider binary.
const fakeProviderEnvVar = "OCEL_TEST_FAKE_PROVIDER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeProviderEnvVar) == "1" {
		os.Exit(runFakeProvider())
	}
	os.Exit(m.Run())
}

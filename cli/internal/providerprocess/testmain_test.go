package providerprocess

import (
	"os"
	"testing"
)

const fakeProviderEnvVar = "OCEL_TEST_PROVIDER_PROCESS"

func TestMain(m *testing.M) {
	if os.Getenv(fakeProviderEnvVar) == "1" {
		os.Exit(runFakeProvider())
	}
	os.Exit(m.Run())
}

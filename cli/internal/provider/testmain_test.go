package provider

import (
	"os"
	"testing"
)

const fakeProviderEnvVar = "OCEL_TEST_FAKE_PROVIDER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeProviderEnvVar) == "1" {
		os.Exit(runFakeProvider())
	}
	os.Exit(m.Run())
}

package constants

import "testing"

func TestProcessEnvironmentNames(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"phase":           "OCEL_PHASE",
		"dev server":      "OCEL_DEV_SERVER",
		"app folder":      "OCEL_APP_FOLDER",
		"app URL":         "OCEL_URL",
		"runtime address": "OCEL_RUNTIME_ADDRESS",
	}
	got := map[string]string{
		"phase":           PhaseEnvName,
		"dev server":      DevServerEnvName,
		"app folder":      AppFolderEnvName,
		"app URL":         AppURLEnvName,
		"runtime address": RuntimeAddressEnvName,
	}
	for name, value := range got {
		if value != want[name] {
			t.Errorf("%s environment name = %q, want %q", name, value, want[name])
		}
	}
}

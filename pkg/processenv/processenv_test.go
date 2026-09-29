package processenv

import "testing"

func TestProcessEnvironmentNames(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"phase":            "OCEL_PHASE",
		"dev server":       "OCEL_DEV_SERVER",
		"dev server token": "OCEL_DEV_SERVER_TOKEN",
		"app folder":       "OCEL_APP_FOLDER",
		"app URL":          "OCEL_URL",
		"runtime address":  "OCEL_RUNTIME_ADDRESS",
	}
	got := map[string]string{
		"phase":            PhaseEnvVar,
		"dev server":       DevServerEnvVar,
		"dev server token": DevServerTokenEnvVar,
		"app folder":       AppFolderEnvVar,
		"app URL":          AppURLEnvVar,
		"runtime address":  RuntimeAddressEnvVar,
	}
	for name, value := range got {
		if value != want[name] {
			t.Errorf("%s environment name = %q, want %q", name, value, want[name])
		}
	}
}

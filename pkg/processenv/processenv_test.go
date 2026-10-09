package processenv

import "testing"

func TestSkipChecksIsOnOnlyWhenItsVariableIsOneOrTrue(t *testing.T) {
	if SkipChecksEnvVar != "OCEL_SKIP_CHECKS" {
		t.Errorf("skip checks environment name = %q, want OCEL_SKIP_CHECKS", SkipChecksEnvVar)
	}
	for value, want := range map[string]bool{"": false, "0": false, "no": false, "1": true, "true": true, "TRUE": true} {
		t.Setenv(SkipChecksEnvVar, value)
		if got := SkipChecks(); got != want {
			t.Errorf("SkipChecks() with %s=%q = %v, want %v", SkipChecksEnvVar, value, got, want)
		}
	}
}

func TestProcessEnvironmentNames(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"phase":            "OCEL_PHASE",
		"dev server":       "OCEL_DEV_SERVER",
		"dev server token": "OCEL_DEV_SERVER_TOKEN",
		"app folder":       "OCEL_APP_FOLDER",
		"app URL":          "OCEL_URL",
		"runtime address":  "OCEL_RUNTIME_ADDRESS",
		"worker":           "OCEL_WORKER",
	}
	got := map[string]string{
		"phase":            PhaseEnvVar,
		"dev server":       DevServerEnvVar,
		"dev server token": DevServerTokenEnvVar,
		"app folder":       AppFolderEnvVar,
		"app URL":          AppURLEnvVar,
		"runtime address":  RuntimeAddressEnvVar,
		"worker":           WorkerEnvVar,
	}
	for name, value := range got {
		if value != want[name] {
			t.Errorf("%s environment name = %q, want %q", name, value, want[name])
		}
	}
}

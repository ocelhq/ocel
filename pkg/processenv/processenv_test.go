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

func TestTheDeploymentURLIsInjectedUnderTheNameEachFrameworkReadsPublicValuesBy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		framework string
		key       string
		want      bool
	}{
		{"every app gets the deployment url under its own name", "go", AppURLEnvVar, true},
		{"a next app gets NEXT_PUBLIC_OCEL_URL", "next", NextPublicURLEnvVar, true},
		{"a next app gets no PUBLIC_OCEL_URL", "next", SvelteKitPublicURLEnvVar, false},
		{"a sveltekit app gets PUBLIC_OCEL_URL", "sveltekit", SvelteKitPublicURLEnvVar, true},
		{"a sveltekit app gets no NEXT_PUBLIC_OCEL_URL", "sveltekit", NextPublicURLEnvVar, false},
		{"a node app gets no NEXT_PUBLIC_OCEL_URL", "node", NextPublicURLEnvVar, false},
		{"a node app gets no PUBLIC_OCEL_URL", "node", SvelteKitPublicURLEnvVar, false},
		{"an app of no named framework gets no NEXT_PUBLIC_OCEL_URL", "", NextPublicURLEnvVar, false},
		{"a go app gets no NEXT_PUBLIC_OCEL_URL", "go", NextPublicURLEnvVar, false},
		{"another name is never injected", "next", "NEXT_PUBLIC_API_URL", false},
	} {
		if got := IsInjected(tc.framework, tc.key); got != tc.want {
			t.Errorf("%s: IsInjected(%q, %q) = %v, want %v", tc.name, tc.framework, tc.key, got, tc.want)
		}
	}
}

func TestOnlyANextPublicPrefixMarksAKeyPublic(t *testing.T) {
	t.Parallel()

	for key, want := range map[string]bool{"NEXT_PUBLIC_API_URL": true, "NEXT_PUBLIC_": true, "PUBLIC_API_URL": false, "API_URL": false, "next_public_x": false} {
		if got := IsNextPublic(key); got != want {
			t.Errorf("IsNextPublic(%q) = %v, want %v", key, got, want)
		}
	}
}

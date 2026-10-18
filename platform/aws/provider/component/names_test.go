package component

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestRoleCoordinate(t *testing.T) {
	t.Parallel()

	coord := AppRoleCoordinate("shop", testStack(t, "prod", "web"))

	if got, want := RolePrefix(coord), "shop-prod-web-app-role-r3f8a1c90-"; got != want {
		t.Errorf("rolePrefix = %q, want %q", got, want)
	}
	if got, want := naming.ResourceID(naming.KindRole, AppRoleLocalName), "role-app"; got != want {
		t.Errorf("resource id = %q, want %q", got, want)
	}
	for _, tc := range []struct {
		parts []string
		want  string
	}{
		{[]string{"policy", "logs"}, "role-app-policy-logs"},
		{[]string{"policy", "isr", "cache"}, "role-app-policy-isr-cache"},
		{[]string{"policy", "variables", "read"}, "role-app-policy-variables-read"},
	} {
		if got := naming.ResourceID(naming.KindRole, AppRoleLocalName, tc.parts...); got != tc.want {
			t.Errorf("resource id = %q, want %q", got, tc.want)
		}
	}
	if got, want := string(Describe(coord, "execution role for this app's functions")),
		"shop / prod / web - execution role for this app's functions - release r3f8a1c90"; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}
}

func TestRolePrefixLeavesRoomForTheSuffix(t *testing.T) {
	t.Parallel()

	long := AppRoleCoordinate(strings.Repeat("p", 30), naming.AppStack(strings.Repeat("e", 30), strings.Repeat("a", 30), fixedRelease(t)))

	got := RolePrefix(long)
	if len(got)+IAMAutonameSuffixLen > MaxRoleNameLen {
		t.Errorf("rolePrefix = %q (%d chars) leaves no room for the %d-character suffix within %d", got, len(got), IAMAutonameSuffixLen, MaxRoleNameLen)
	}
	if !strings.HasSuffix(got, naming.WordSeparator) {
		t.Errorf("rolePrefix = %q, want it to end on the word separator", got)
	}
}

func fixedRelease(t *testing.T) naming.ReleaseToken {
	t.Helper()
	release, err := naming.ParseReleaseToken("r3f8a1c90")
	if err != nil {
		t.Fatalf("parse release: %v", err)
	}
	return release
}

func testStack(t *testing.T, env, app string) naming.StackName {
	t.Helper()
	return naming.AppStack(env, app, fixedRelease(t))
}

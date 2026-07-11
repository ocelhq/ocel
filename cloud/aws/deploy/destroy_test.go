package deploy

import "testing"

func TestPreviewIdentityFromStack(t *testing.T) {
	cases := []struct {
		name      string
		projectID string
		stack     string
		wantID    string
		wantMatch bool
	}{
		{"preview stack", "proj_123", "proj_123-preview-feature_login_ab12", "feature_login_ab12", true},
		{"production stack is not a preview", "proj_123", "proj_123-prod", "", false},
		{"another project's preview is excluded", "proj_123", "proj_999-preview-x", "", false},
		{"empty identity", "proj_123", "proj_123-preview-", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotID, gotMatch := previewIdentityFromStack(tc.projectID, tc.stack)
			if gotID != tc.wantID || gotMatch != tc.wantMatch {
				t.Errorf("previewIdentityFromStack(%q, %q) = (%q, %v), want (%q, %v)",
					tc.projectID, tc.stack, gotID, gotMatch, tc.wantID, tc.wantMatch)
			}
		})
	}
}

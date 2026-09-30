package router

import "testing"

func TestADeploymentPointerParsesBackIntoItsPreviewAndPromotion(t *testing.T) {
	t.Parallel()

	preview, promotionID, ok := ParseDeploymentPointer(FormatDeploymentPointer("pr-42", "p1"))
	if !ok || preview != "pr-42" || promotionID != "p1" {
		t.Errorf("ParseDeploymentPointer = %q, %q, %v, want pr-42, p1", preview, promotionID, ok)
	}
	for _, pointer := range []string{"pr-42", "", DefaultPointer} {
		if preview, promotionID, ok := ParseDeploymentPointer(pointer); ok {
			t.Errorf("ParseDeploymentPointer(%q) = %q, %q, want no deployment pointer", pointer, preview, promotionID)
		}
	}
}

package naming

import (
	"strings"
	"testing"
)

func TestValidateDeploymentID(t *testing.T) {
	t.Run("accepts a minted id", func(t *testing.T) {
		for _, id := range []string{
			"00000000000000000000000000000000",
			"d1a2b3c4d5e6f708192a3b4c5d6e7f80",
			"ffffffffffffffffffffffffffffffff",
		} {
			if err := ValidateDeploymentID(id); err != nil {
				t.Errorf("ValidateDeploymentID(%q) = %v, want nil", id, err)
			}
		}
	})

	t.Run("rejects anything else", func(t *testing.T) {
		for _, id := range []string{
			"",
			"dep1",
			"D1A2B3C4D5E6F708192A3B4C5D6E7F80",
			"d1a2b3c4d5e6f708192a3b4c5d6e7f8",
			"d1a2b3c4d5e6f708192a3b4c5d6e7f800",
			"d1a2b3c4d5e6f708192a3b4c5d6e7f8g",
			"d1a2b3c4-d5e6-f708-192a-3b4c5d6e7f",
			"d1a2b3c4d5e6f708192a3b4c5d6e7f80\n",
			"../../etc/passwd",
			strings.Repeat("a", 64),
		} {
			if err := ValidateDeploymentID(id); err == nil {
				t.Errorf("ValidateDeploymentID(%q) = nil, want an error", id)
			}
		}
	})
}

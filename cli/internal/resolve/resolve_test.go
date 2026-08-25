package resolve

import (
	"context"
	"testing"
)

func TestStubAccount(t *testing.T) {
	t.Parallel()

	t.Run("returns an identity for the project ID", func(t *testing.T) {
		t.Parallel()
		cfg, err := StubAccount(context.Background(), "https://api.example.com", "tok_123", "proj_abc")
		if err != nil {
			t.Fatalf("StubAccount: %v", err)
		}
		if cfg.ProjectID != "proj_abc" {
			t.Fatalf("ProjectID = %q, want %q", cfg.ProjectID, "proj_abc")
		}
		if cfg.OrgID == "" {
			t.Fatal("OrgID is empty")
		}
		if cfg.UserID == "" {
			t.Fatal("UserID is empty")
		}
		if cfg.APIURL != "https://api.example.com" {
			t.Fatalf("APIURL = %q, want %q", cfg.APIURL, "https://api.example.com")
		}
		if cfg.Token != "tok_123" {
			t.Fatalf("Token = %q, want %q", cfg.Token, "tok_123")
		}
	})
}

package prompt

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestConfirmPhrase(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		label  string
		phrase string
		input  string
		want   bool
		prompt string
	}{
		{"the exact project name proceeds", "project name", "proj_shop", "proj_shop\n", true, "Type the project name (proj_shop) to confirm:"},
		{"surrounding space is trimmed", "project name", "proj_shop", "  proj_shop  \n", true, "Type the project name (proj_shop) to confirm:"},
		{"a near miss aborts", "project name", "proj_shop", "proj_shopp\n", false, "Type the project name (proj_shop) to confirm:"},
		{"reflexive yes aborts", "project name", "proj_shop", "y\n", false, "Type the project name (proj_shop) to confirm:"},
		{"empty aborts", "project name", "proj_shop", "\n", false, "Type the project name (proj_shop) to confirm:"},
		{"closed stdin aborts", "project name", "proj_shop", "", false, "Type the project name (proj_shop) to confirm:"},
		{"the base domain is its own scope", "domain", "preview.acme.com", "preview.acme.com\n", true, "Type the domain (preview.acme.com) to confirm:"},
		{"another scope's phrase does not carry", "domain", "preview.acme.com", "proj_shop\n", false, "Type the domain (preview.acme.com) to confirm:"},
		{"the class name is the bootstrap's phrase", "class name", "preview", "preview\n", true, "Type the class name (preview) to confirm:"},
		{"the other class does not confirm this one", "class name", "preview", "production\n", false, "Type the class name (preview) to confirm:"},
		{"a phrase the provider never sent confirms nothing", "project name", "", "\n", false, "Type the project name () to confirm:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			got, err := New(&stdout, strings.NewReader(tc.input)).Phrase(context.Background(), tc.label, tc.phrase)
			if err != nil {
				t.Fatalf("Phrase() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("Phrase(%q, %q) = %v, want %v", tc.phrase, tc.input, got, tc.want)
			}
			if !strings.Contains(stdout.String(), tc.prompt) {
				t.Errorf("stdout = %q, want the prompt %q", stdout.String(), tc.prompt)
			}
		})
	}
}

package target

import "testing"

func TestFingerprintFormats(t *testing.T) {
	cases := []struct {
		name string
		made func() (string, error)
		want string
	}{
		{"aws", func() (string, error) { return Fingerprint("aws", "123456789012", "us-east-1", "main") }, "aws/123456789012/us-east-1/main"},
		{"gcp", func() (string, error) { return Fingerprint("gcp", "ocel-prod", "europe-west1", "main") }, "gcp/ocel-prod/europe-west1/main"},
		{"vps", func() (string, error) { return Fingerprint("vps", "sha256:abc", "main") }, "vps/sha256:abc/main"},
		{"one part", func() (string, error) { return Fingerprint("fly", "iad") }, "fly/iad"},
	}
	for _, held := range cases {
		t.Run(held.name, func(t *testing.T) {
			got, err := held.made()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != held.want {
				t.Errorf("got %q, want %q", got, held.want)
			}
		})
	}
}

func TestFingerprintRefusals(t *testing.T) {
	cases := []struct {
		name string
		made func() (string, error)
	}{
		{"no vendor", func() (string, error) { return Fingerprint("", "a") }},
		{"an upper-case vendor", func() (string, error) { return Fingerprint("AWS", "a") }},
		{"a slash in the vendor", func() (string, error) { return Fingerprint("aws/two", "a") }},
		{"no parts", func() (string, error) { return Fingerprint("aws") }},
		{"an empty part", func() (string, error) { return Fingerprint("aws", "123456789012", "", "main") }},
		{"a slash inside a part", func() (string, error) { return Fingerprint("aws", "123456789012", "us-east-1", "main/two") }},
		{"whitespace inside a part", func() (string, error) { return Fingerprint("gcp", "ocel prod", "europe-west1", "main") }},
		{"an upper-case part", func() (string, error) { return Fingerprint("vps", "SHA256:abc", "main") }},
		{"base64 padding inside a part", func() (string, error) { return Fingerprint("vps", "sha256:ab+c/d=", "main") }},
	}
	for _, held := range cases {
		t.Run(held.name, func(t *testing.T) {
			if got, err := held.made(); err == nil {
				t.Fatalf("got %q, want a refusal", got)
			}
		})
	}
}

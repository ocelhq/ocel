package target

import "testing"

func TestFingerprintFormats(t *testing.T) {
	cases := []struct {
		name string
		made func() (string, error)
		want string
	}{
		{"aws", func() (string, error) { return ForAWS("123456789012", "us-east-1", "main") }, "aws/123456789012/us-east-1/main"},
		{"gcp", func() (string, error) { return ForGCP("ocel-prod", "europe-west1", "main") }, "gcp/ocel-prod/europe-west1/main"},
		{"vps", func() (string, error) { return ForVPS("SHA256:abc", "main") }, "vps/SHA256:abc/main"},
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
		{"an unknown vendor", func() (string, error) { return Fingerprint("azure", "a", "b", "c") }},
		{"too few parts", func() (string, error) { return Fingerprint(AWS, "123456789012", "us-east-1") }},
		{"too many parts", func() (string, error) { return Fingerprint(VPS, "SHA256:abc", "main", "extra") }},
		{"an empty part", func() (string, error) { return ForAWS("123456789012", "", "main") }},
		{"a slash inside a part", func() (string, error) { return ForAWS("123456789012", "us-east-1", "main/two") }},
		{"whitespace inside a part", func() (string, error) { return ForGCP("ocel prod", "europe-west1", "main") }},
	}
	for _, held := range cases {
		t.Run(held.name, func(t *testing.T) {
			if got, err := held.made(); err == nil {
				t.Fatalf("got %q, want a refusal", got)
			}
		})
	}
}

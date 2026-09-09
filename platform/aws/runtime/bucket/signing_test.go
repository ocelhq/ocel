package bucket

import "testing"

var testFile = SignedFile{
	Key:      "org/proj/user/avatar.png",
	Name:     "avatar.png",
	Size:     1024,
	MimeType: "image/png",
}

func mustSign(t *testing.T, secret, sessionID string, file SignedFile) string {
	t.Helper()
	sig, err := signUpload(secret, sessionID, file)
	if err != nil {
		t.Fatalf("signUpload: %v", err)
	}
	return sig
}

func mustVerify(t *testing.T, secret, sessionID string, file SignedFile, signature string) bool {
	t.Helper()
	ok, err := verifyUpload(secret, sessionID, file, signature)
	if err != nil {
		t.Fatalf("verifyUpload: %v", err)
	}
	return ok
}

func TestCanonicalUploadPayload(t *testing.T) {
	t.Parallel()

	t.Run("matches the TS scheme byte for byte", func(t *testing.T) {
		t.Parallel()
		const want = `{"sessionId":"sess_1","file":{"key":"org/proj/user/avatar.png","name":"avatar.png","size":1024,"mimeType":"image/png"}}`
		got, err := CanonicalUploadPayload("sess_1", testFile)
		if err != nil {
			t.Fatalf("CanonicalUploadPayload: %v", err)
		}
		if string(got) != want {
			t.Fatalf("canonical payload mismatch\n got: %s\nwant: %s", got, want)
		}
	})

	t.Run("leaves HTML-significant characters unescaped, as JSON.stringify does", func(t *testing.T) {
		t.Parallel()
		const want = `{"sessionId":"sess_1","file":{"key":"a<b>c&d","name":"a<b>c&d","size":1,"mimeType":"text/plain"}}`
		got, err := CanonicalUploadPayload("sess_1", SignedFile{Key: "a<b>c&d", Name: "a<b>c&d", Size: 1, MimeType: "text/plain"})
		if err != nil {
			t.Fatalf("CanonicalUploadPayload: %v", err)
		}
		if string(got) != want {
			t.Fatalf("canonical payload mismatch\n got: %s\nwant: %s", got, want)
		}
	})
}

func TestVerifyUpload(t *testing.T) {
	t.Parallel()

	t.Run("a signature made with the secret round trips", func(t *testing.T) {
		t.Parallel()
		sig := mustSign(t, "s3cret", "sess_1", testFile)
		if !mustVerify(t, "s3cret", "sess_1", testFile, sig) {
			t.Fatal("a signature made with the secret should verify")
		}
	})

	tampered := testFile
	tampered.Size = 2048

	cases := []struct {
		name      string
		secret    string
		sessionID string
		file      SignedFile
		signature func(t *testing.T) string
	}{
		{
			name: "a different secret", secret: "other-secret", sessionID: "sess_1", file: testFile,
			signature: func(t *testing.T) string { return mustSign(t, "s3cret", "sess_1", testFile) },
		},
		{
			name: "a tampered file identity", secret: "s3cret", sessionID: "sess_1", file: tampered,
			signature: func(t *testing.T) string { return mustSign(t, "s3cret", "sess_1", testFile) },
		},
		{
			name: "another session's id", secret: "s3cret", sessionID: "sess_2", file: testFile,
			signature: func(t *testing.T) string { return mustSign(t, "s3cret", "sess_1", testFile) },
		},
		{
			name: "garbage, without panicking", secret: "s3cret", sessionID: "sess_1", file: testFile,
			signature: func(*testing.T) string { return "deadbeef" },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if mustVerify(t, tc.secret, tc.sessionID, tc.file, tc.signature(t)) {
				t.Fatalf("%s must not verify", tc.name)
			}
		})
	}
}

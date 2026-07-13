package bucket

import "testing"

var testFile = SignedFile{
	Key:      "org/proj/user/avatar.png",
	Name:     "avatar.png",
	Size:     1024,
	MimeType: "image/png",
}

func TestCanonicalUploadPayloadMatchesTSScheme(t *testing.T) {
	// Byte-identical to the dev signer's canonicalUploadPayload assertion in
	// packages/api/src/routes/blob/signing.test.ts.
	const want = `{"sessionId":"sess_1","file":{"key":"org/proj/user/avatar.png","name":"avatar.png","size":1024,"mimeType":"image/png"}}`
	got := string(CanonicalUploadPayload("sess_1", testFile))
	if got != want {
		t.Fatalf("canonical payload mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestVerifyUploadRoundTrips(t *testing.T) {
	sig := signUpload("s3cret", "sess_1", testFile)
	if !verifyUpload("s3cret", "sess_1", testFile, sig) {
		t.Fatal("a signature made with the secret should verify")
	}
}

func TestVerifyUploadRejectsWrongSecret(t *testing.T) {
	sig := signUpload("s3cret", "sess_1", testFile)
	if verifyUpload("other-secret", "sess_1", testFile, sig) {
		t.Fatal("a signature made with a different secret must not verify")
	}
}

func TestVerifyUploadRejectsTamperedFile(t *testing.T) {
	sig := signUpload("s3cret", "sess_1", testFile)
	tampered := testFile
	tampered.Size = 2048
	if verifyUpload("s3cret", "sess_1", tampered, sig) {
		t.Fatal("a tampered file identity must not verify")
	}
}

func TestVerifyUploadRejectsGarbageWithoutPanic(t *testing.T) {
	if verifyUpload("s3cret", "sess_1", testFile, "deadbeef") {
		t.Fatal("a garbage signature must not verify")
	}
}

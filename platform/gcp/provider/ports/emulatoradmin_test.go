package ports

import "testing"

func TestAdminCredentialsAreOfferedOnlyToALoopbackEmulator(t *testing.T) {
	tests := []struct {
		endpoint string
		offered  bool
	}{
		{"http://127.0.0.1:8085", true},
		{"localhost:8085", true},
		{"http://[::1]:8085", true},
		{"", false},
		{"http://10.0.0.5:8085", false},
		{"https://firestore.googleapis.com", false},
		{"http://127.0.0.1.evil.example:8085", false},
	}
	for _, test := range tests {
		t.Run(test.endpoint, func(t *testing.T) {
			if got := len(EmulatorAdmin(test.endpoint)) > 0; got != test.offered {
				t.Errorf("EmulatorAdmin(%q) offers credentials = %v, want %v: they are a plaintext password to whatever answers there", test.endpoint, got, test.offered)
			}
		})
	}
}

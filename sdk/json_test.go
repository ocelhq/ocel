package ocel

import "testing"

func TestJSONEncodingKeepsAnEscapedBackslashBeforeTheTextU2028(t *testing.T) {
	text := `\u` + "2028 " + `\\u` + "2029 \xe2\x80\xa8"

	encoded, err := encodeJSON(text)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"\\u` + `2028 \\\\u` + "2029 \xe2\x80\xa8\""; string(encoded) != want {
		t.Errorf("encoded = %s, want %s", encoded, want)
	}
}

package redaction

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestHideReplacesEveryValueWhereverItIsSaid(t *testing.T) {
	values := NewValues([]string{"sk_live_1", "ss_live_2"})

	got := values.Hide("charging with sk_live_1, signing with ss_live_2 and sk_live_1 again")

	want := "charging with [secret], signing with [secret] and [secret] again"
	if got != want {
		t.Errorf("Hide = %q, want %q", got, want)
	}
}

func TestHideReplacesAValueWhereALongerOneContainsIt(t *testing.T) {
	values := NewValues([]string{"abc", "abcdef"})

	if got := values.Hide("key abcdef"); got != "key [secret]" {
		t.Errorf("Hide = %q, want the longer value hidden whole", got)
	}
}

func TestHideReplacesEachLineOfAMultilineValueAndItsJSONForm(t *testing.T) {
	pem := "-----BEGIN KEY-----\nMIIEvQIBADAN\n-----END KEY-----"
	values := NewValues([]string{pem, `quote"d`})

	got := values.Hide("line 2 alone: MIIEvQIBADAN\n" + `{"message":"bad quote\"d"}`)

	for _, leaked := range []string{"MIIEvQIBADAN", `quote\"d`} {
		if strings.Contains(got, leaked) {
			t.Errorf("Hide = %q, still holds %q", got, leaked)
		}
	}
}

func TestHideReplacesAValueAsGoAndJavaScriptEachWriteItInJSON(t *testing.T) {
	values := NewValues([]string{"pa<ss\"wo&rd x"})

	for writer, said := range map[string]string{
		"go's json.Marshal":                  `{"key":"pa<ss\"wo&rd x"}`,
		"go's json.Encoder without escaping": `{"key":"pa<ss\"wo&rd x"}`,
		"javascript's JSON.stringify":        "{\"key\":\"pa<ss\\\"wo&rd x\"}",
	} {
		if got := values.Hide(said); got != `{"key":"[secret]"}` {
			t.Errorf("Hide of the value as %s writes it = %q, want it hidden", writer, got)
		}
	}
}

func TestHideLeavesTextAloneWhenThereIsNothingToHide(t *testing.T) {
	if got := (Values{}).Hide("plain build output"); got != "plain build output" {
		t.Errorf("Hide = %q, want the text unchanged", got)
	}
	if got := NewValues([]string{""}).Hide("plain build output"); got != "plain build output" {
		t.Errorf("Hide with an empty value = %q, want the text unchanged", got)
	}
}

func TestAWriterHidesEachWriteBeforePassingItOn(t *testing.T) {
	var out bytes.Buffer
	w := NewValues([]string{"ss_live"}).Writer(&out)

	if _, err := w.Write([]byte("secret is ss_live\n")); err != nil {
		t.Fatal(err)
	}

	if out.String() != "secret is [secret]\n" {
		t.Errorf("wrote %q, want the value hidden", out.String())
	}
}

func TestAWriterHidesAValueSplitAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	w := NewValues([]string{"sk_live_123"}).Writer(&out)

	for _, chunk := range []string{"key sk_li", "ve_1", "23 used\nnext sk_live", "_123"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	if want := "key [secret] used\nnext [secret]"; out.String() != want {
		t.Errorf("wrote %q, want %q", out.String(), want)
	}
}

func TestAWriterPassesEachFinishedLineOnAtOnce(t *testing.T) {
	var out bytes.Buffer
	w := NewValues([]string{"sk_live_123"}).Writer(&out)

	if _, err := w.Write([]byte("compiled\nlinking")); err != nil {
		t.Fatal(err)
	}

	if out.String() != "compiled\n" {
		t.Errorf("wrote %q before Flush, want the finished line", out.String())
	}
}

func TestAWriterWithNothingToHidePassesEveryWriteOn(t *testing.T) {
	var out bytes.Buffer
	w := Values{}.Writer(&out)

	if _, err := w.Write([]byte("linking")); err != nil {
		t.Fatal(err)
	}

	if out.String() != "linking" {
		t.Errorf("wrote %q, want the write passed on", out.String())
	}
}

func TestHideErrorHidesTheMessageAndKeepsTheChain(t *testing.T) {
	cause := errors.New("next build failed: ss_live")

	err := NewValues([]string{"ss_live"}).HideError(cause)

	if err.Error() != "next build failed: [secret]" {
		t.Errorf("Error() = %q, want the value hidden", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is(err, cause) = false, want the cause still reachable")
	}
	if NewValues([]string{"ss_live"}).HideError(nil) != nil {
		t.Error("HideError(nil) != nil")
	}
}

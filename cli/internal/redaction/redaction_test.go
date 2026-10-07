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

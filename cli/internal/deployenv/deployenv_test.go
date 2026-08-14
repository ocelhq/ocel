package deployenv

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	t.Run("reads a JSON object of string values", func(t *testing.T) {
		t.Parallel()

		got, err := Parse(`{"MIDDLEWARE_TEST":"asdf","STRING_ENV_VAR":"asdf3"}`)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		want := map[string]string{"MIDDLEWARE_TEST": "asdf", "STRING_ENV_VAR": "asdf3"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Parse = %v, want %v", got, want)
		}
	})

	t.Run("treats blank and empty as unset", func(t *testing.T) {
		t.Parallel()

		for _, raw := range []string{"", "  ", "{}"} {
			got, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse(%q): %v", raw, err)
			}
			if got != nil {
				t.Errorf("Parse(%q) = %v, want nothing to deliver", raw, got)
			}
		}
	})

	t.Run("rejects anything that is not an object of strings", func(t *testing.T) {
		t.Parallel()

		for _, raw := range []string{`["A=1"]`, `{"A":1}`, `"A=1"`, `{`} {
			if _, err := Parse(raw); err == nil {
				t.Errorf("Parse(%q) succeeded; want the shape rejected", raw)
			} else if !strings.Contains(err.Error(), EnvVar) {
				t.Errorf("Parse(%q) error = %q, want it to name %s", raw, err, EnvVar)
			}
		}
	})

	t.Run("rejects a key no environment can carry", func(t *testing.T) {
		t.Parallel()

		for _, raw := range []string{`{"":"v"}`, `{"A=B":"v"}`, `{"A B":"v"}`} {
			if _, err := Parse(raw); err == nil {
				t.Errorf("Parse(%q) succeeded; want the key rejected", raw)
			}
		}
	})
}

func TestKeys(t *testing.T) {
	t.Parallel()

	got := Keys(map[string]string{"B": "2", "A": "1", "C": "3"})
	want := []string{"A", "B", "C"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Keys = %v, want %v", got, want)
	}
}

package host

import (
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestARealStoreSettlesASessionWithIfMatchAndRefusesAStaleOne(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	store := anExternalStore(t, "conditional-bucket")
	now := time.Now().UTC()
	const key = "session.json"

	opened, err := store.call("opened", "PUT", key, "", map[string]string{"If-None-Match": "*"}, []byte(probeBody), true, now)
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.call("read", "HEAD", key, "", nil, nil, true, now)
	if err != nil {
		t.Fatal(err)
	}
	said, err := hereRuns(t)("open a session on the store", probeScript([]probeCall{opened, read}))
	if err != nil {
		t.Fatal(err)
	}
	if code := readProbe(said)["opened"].code; !slices.Contains(storeWrote, code) {
		t.Fatalf("the store answered %s claiming a session key it held nothing under", code)
	}

	settled, err := store.call("settled", "PUT", key, "", map[string]string{"If-Match": "\"nothing-like-the-etag\""}, []byte(probeBody), false, now)
	if err != nil {
		t.Fatal(err)
	}
	said, err = hereRuns(t)("settle a session another replica already moved", probeScript([]probeCall{settled}))
	if err != nil {
		t.Fatal(err)
	}
	if code := readProbe(said)["settled"].code; code != "412" {
		t.Errorf("the store answered %s to a write conditional on an etag it does not hold, and two replicas would both fire onUploadComplete", code)
	}
}

func anAbsentStore() ExternalStore {
	return ExternalStore{
		Endpoint: "http://store.invalid", Region: "us-east-1", Bucket: "theirs",
		AccessKeyID: "a", SecretKey: "b", PathStyle: true,
	}
}

type scriptedProbe struct {
	answers []string
	fail    error
	ran     []string
}

func (s *scriptedProbe) run(what, script string) (string, error) {
	s.ran = append(s.ran, script)
	if len(s.answers) == 0 {
		return "", nil
	}
	said := s.answers[0]
	s.answers = s.answers[1:]
	if s.fail != nil && strings.Contains(script, "'conditional-put'") {
		return said, s.fail
	}
	return said, nil
}

func (s *scriptedProbe) drove(name string) bool {
	for _, script := range s.ran {
		if strings.Contains(script, "'"+name+"'") {
			return true
		}
	}
	return false
}

func corsAnswer(code string, body string) string {
	return "cors-before=" + code + "\ncors-before.body=" + base64.StdEncoding.EncodeToString([]byte(body)) + "\n"
}

const theirCors = `<CORSConfiguration><CORSRule><AllowedOrigin>https://theirs.example.com</AllowedOrigin></CORSRule></CORSConfiguration>`

func TestAStoreWhoseCorsCouldNotBeReadKeepsWhateverItHas(t *testing.T) {
	t.Parallel()

	for _, said := range []string{corsAnswer("403", ""), corsAnswer("500", "boom"), "cors-before=no-answer\n"} {
		driver := &scriptedProbe{answers: []string{said}}
		_, err := probeExternalStore(anAbsentStore(), probePrefix+"held/", time.Now().UTC(), driver.run)
		if err == nil {
			t.Fatalf("a store that would not say what origins it answers was accepted: %q", said)
		}
		if driver.drove("cors-drop") {
			t.Errorf("the probe deleted a CORS configuration it never read, on %q", said)
		}
		if driver.drove("cors-put") {
			t.Errorf("the probe overwrote a CORS configuration it never read, on %q", said)
		}
		if !strings.Contains(err.Error(), "Cors") {
			t.Errorf("the refusal never names what the store could not do:\n%s", err)
		}
	}
}

func TestAStoreThatAlreadyAnswersOriginsIsHandedItsOwnConfigurationBack(t *testing.T) {
	t.Parallel()

	driver := &scriptedProbe{answers: []string{corsAnswer("200", theirCors), ""}}
	if _, err := probeExternalStore(anAbsentStore(), probePrefix+"held/", time.Now().UTC(), driver.run); err == nil {
		t.Fatal("a store answering nothing else was accepted")
	}
	if driver.drove("cors-drop") {
		t.Fatal("the probe deleted the customer's own CORS configuration")
	}
	if !driver.drove("cors-restore") {
		t.Fatal("the probe never put the customer's own CORS configuration back")
	}
	restored := driver.ran[len(driver.ran)-1]
	if !strings.Contains(restored, base64.StdEncoding.EncodeToString([]byte(theirCors))) {
		t.Errorf("the probe put something other than what it read back:\n%s", restored)
	}
	ours, err := corsBody([]string{probeOrigin})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(driver.ran, "\n"), base64.StdEncoding.EncodeToString(ours)) {
		t.Error("the probe wrote an origin of its own over a configuration the customer already had")
	}
}

func TestAProbeThatFailsPartWayThroughStillTakesItselfBackDown(t *testing.T) {
	t.Parallel()

	driver := &scriptedProbe{
		answers: []string{corsAnswer("404", ""), "multipart-create=200\n"},
		fail:    errors.New("the box lost its connection"),
	}
	_, err := probeExternalStore(anAbsentStore(), probePrefix+"held/", time.Now().UTC(), driver.run)
	if err == nil {
		t.Fatal("a probe the box could not finish was accepted")
	}
	if !driver.drove("delete-conditional") || !driver.drove("cors-drop") {
		t.Fatalf("a probe that failed left its own objects and CORS rule behind: %d scripts ran", len(driver.ran))
	}
}

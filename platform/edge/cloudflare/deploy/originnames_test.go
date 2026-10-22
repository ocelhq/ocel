package cloudflare

import "testing"

var (
	testNames  = originNames{namespace: defaultNamespace, origin: testOrigin}
	otherNames = originNames{namespace: defaultNamespace, origin: otherOrigin}
)

func TestAnEdgeOpenedWithoutANamespaceNamesNothing(t *testing.T) {
	t.Parallel()

	if names, err := newCloudflare("", fixedOrigin(testOrigin), Options{}).resolveNames(t.Context()); err == nil {
		t.Errorf("names() = %+v, want an error: a name without its namespace reaches whatever another namespace deployed", names)
	}
}

func TestAnEdgeNamesWhatItKeepsForTheNamespaceAndOriginItWasOpenedFor(t *testing.T) {
	t.Parallel()

	names, err := newCloudflare(defaultNamespace, fixedOrigin(testOrigin), Options{}).resolveNames(t.Context())
	if err != nil {
		t.Fatalf("names(): %v", err)
	}
	if names != testNames {
		t.Errorf("names() = %+v, want %+v", names, testNames)
	}
}

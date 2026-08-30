package conformance

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

type loadingProvider struct {
	*fake.Provider
	direct *fake.Images
}

func (p loadingProvider) DirectImages(context.Context) (providerkit.ImageStore, error) {
	return p.direct, nil
}

func takesImagesDirectly(t *testing.T) loadingProvider {
	t.Helper()
	return loadingProvider{
		Provider: fake.NewProvider(fake.Options{Region: "nowhere"}),
		direct:   fake.NewImages(),
	}
}

type pullingProvider struct {
	*fake.Provider
	pulled *fake.Images
}

func (p pullingProvider) Images(context.Context, providerkit.RegistryTarget) (providerkit.ImageStore, error) {
	return p.pulled, nil
}

func makesTheMachinePull(t *testing.T) pullingProvider {
	t.Helper()
	return pullingProvider{
		Provider: fake.NewProvider(fake.Options{Region: "nowhere"}),
		pulled:   fake.NewImages(),
	}
}

func TestTheOptionalSweepDrivesAProviderThatMakesTheMachinePull(t *testing.T) {
	provider := makesTheMachinePull(t)

	var swept bool
	for _, set := range optionalSets {
		if set.name != "ImagePusher" {
			continue
		}
		swept = true
		if !set.onRoot(provider) {
			t.Error("the sweep does not see Images on a provider that declares it, so the port it opens is covered by nothing")
		}
		for _, port := range wrapPorts(t, provider) {
			if set.onPort(port.value) {
				t.Errorf("Images is reachable through the wrapped %s port, which only holds while nothing wraps it", port.name)
			}
		}
	}
	if !swept {
		t.Fatal("no optional set names ImagePusher, so a provider that carries an image through a registry passes the sweep by being scanned for nothing")
	}
}

func TestTheOptionalSweepDrivesAProviderThatTakesImagesDirectly(t *testing.T) {
	provider := takesImagesDirectly(t)

	var swept bool
	for _, set := range optionalSets {
		if set.name != "ImageLoader" {
			continue
		}
		swept = true
		if !set.onRoot(provider) {
			t.Error("the sweep does not see DirectImages on a provider that declares it, so the port it opens is covered by nothing")
		}
		for _, port := range wrapPorts(t, provider) {
			if set.onPort(port.value) {
				t.Errorf("DirectImages is reachable through the wrapped %s port, which only holds while nothing wraps it", port.name)
			}
		}
	}
	if !swept {
		t.Fatal("no optional set names ImageLoader, so a provider that takes an image directly passes the sweep by being scanned for nothing")
	}
}

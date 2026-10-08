package images

import "github.com/ocelhq/ocel/pkg/provider"

var RefuseForeignTokenRealm = refuseForeignTokenRealm

var Resolvable = resolvable

var RegistryTimeout = &registryTimeout

func RegistryStoreAnsweredBy(target provider.RegistryTarget, packagesAPI string) provider.ImageStore {
	return registryStore{target: target, packagesAPI: packagesAPI}
}

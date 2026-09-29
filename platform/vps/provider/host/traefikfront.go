package host

import "github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"

func (f Front) traefik(box traefik.Box) traefik.Traefik {
	return traefik.Traefik{
		Box:                box,
		Directory:          f.Traefik.Directory,
		ContainerDirectory: f.Traefik.ContainerDirectory,
		Resolver:           f.Traefik.Resolver,
		PreviewResolver:    f.Traefik.PreviewResolver,
		HTTP:               f.Traefik.Entrypoints.HTTP,
		HTTPS:              f.Traefik.Entrypoints.HTTPS,
		Network:            f.Traefik.Network,
		Port:               f.Traefik.Port,
	}
}

package caddy

func StoreCertificatesAt(dir string) func() {
	was := certificateStore
	certificateStore = dir
	return func() { certificateStore = was }
}

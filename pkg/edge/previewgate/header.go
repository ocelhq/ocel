package previewgate

type Header map[string][]string

func (h Header) Add(name, value string) {
	name = canonicalName(name)
	h[name] = append(h[name], value)
}

func (h Header) Set(name, value string) {
	h[canonicalName(name)] = []string{value}
}

func (h Header) Get(name string) string {
	if values := h[canonicalName(name)]; len(values) > 0 {
		return values[0]
	}
	return ""
}

func (h Header) Values(name string) []string {
	return h[canonicalName(name)]
}

func (h Header) Del(name string) {
	delete(h, canonicalName(name))
}

func (h Header) Clone() Header {
	if h == nil {
		return nil
	}
	cloned := make(Header, len(h))
	for name, values := range h {
		cloned[name] = append([]string(nil), values...)
	}
	return cloned
}

func canonicalName(name string) string {
	canonical := []byte(name)
	upper := true
	for i, b := range canonical {
		switch {
		case upper && 'a' <= b && b <= 'z':
			canonical[i] = b - 'a' + 'A'
		case !upper && 'A' <= b && b <= 'Z':
			canonical[i] = b - 'A' + 'a'
		}
		upper = b == '-'
	}
	return string(canonical)
}

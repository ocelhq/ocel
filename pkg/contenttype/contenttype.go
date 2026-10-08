package contenttype

import "strings"

const unknown = "application/octet-stream"

var byExtension = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json; charset=utf-8",
	".map":         "application/json; charset=utf-8",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".eot":         "application/vnd.ms-fontobject",
	".txt":         "text/plain; charset=utf-8",
	".xml":         "application/xml",
	".webmanifest": "application/manifest+json",
	".wasm":        "application/wasm",
}

var byName = map[string]string{
	"robots.txt":    "text/plain",
	"manifest.json": "application/manifest+json",
}

func Infer(path string) string {
	name := strings.ToLower(path[strings.LastIndex(path, "/")+1:])
	if ct, ok := byName[name]; ok {
		return ct
	}
	dot := strings.LastIndex(name, ".")
	if dot == -1 {
		return unknown
	}
	if ct, ok := byExtension[name[dot:]]; ok {
		return ct
	}
	return unknown
}

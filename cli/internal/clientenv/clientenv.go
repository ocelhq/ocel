// Package clientenv is the browser half of a project's variables: the one
// place that knows how a client-accessible value reaches a browser bundle.
//
// The mechanism is the framework's own. A value is exported to the app build
// under the framework's public prefix, and the accessor generated into the app
// names that prefixed entry literally, so the framework's static replacement
// does the inlining — no custom build-step transform to track across framework
// versions.
package clientenv

// PublicPrefix is the framework's own public prefix: the environment names it
// will inline into a browser bundle, and the only ones it will.
const PublicPrefix = "NEXT_PUBLIC_"

// PublicName is the build-environment entry a client-accessible key is
// delivered under. The key's own name is kept whole, so a project reading
// `process.env.PUBLIC_SITE_URL` itself and a project reading it through the
// accessor never disagree about which variable is which.
func PublicName(key string) string { return PublicPrefix + key }

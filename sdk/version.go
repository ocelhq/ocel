package ocel

import "runtime/debug"

const modulePath = "ocel.dev"

var version = versionIn(debug.ReadBuildInfo())

func versionIn(info *debug.BuildInfo, ok bool) string {
	if !ok {
		return ""
	}
	if info.Main.Path == modulePath {
		return info.Main.Version
	}
	for _, dep := range info.Deps {
		if dep.Path != modulePath {
			continue
		}
		if dep.Replace != nil {
			return dep.Replace.Version
		}
		return dep.Version
	}
	return ""
}

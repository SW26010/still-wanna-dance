// Package buildinfo exposes the same executable identity to diagnostics and the UI.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// Set by the desktop build scripts. Direct go builds remain development builds.
var (
	Version        = "dev"
	Kind           = "development"
	BuiltAtUTC     = ""
	SourceRevision = "" // Only source-archive rebuilds lack Git build settings.
)

type Info struct {
	Kind       string `json:"kind"`
	Version    string `json:"version"`
	Revision   string `json:"revision"`
	Dirty      bool   `json:"dirty"`
	BuiltAtUTC string `json:"builtAtUtc"`
	Toolchain  string `json:"toolchain"`
	Target     string `json:"target"`
}

func Current() Info {
	build, _ := debug.ReadBuildInfo()
	return fromBuild(build)
}

func fromBuild(build *debug.BuildInfo) Info {
	info := Info{Kind: Kind, Version: Version, Dirty: true,
		BuiltAtUTC: BuiltAtUTC, Toolchain: runtime.Version(), Target: runtime.GOOS + "/" + runtime.GOARCH}
	if Kind == "rebuild" && SourceRevision != "" {
		info.Revision = SourceRevision
		info.Dirty = false
	}
	if build != nil {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				info.Revision = setting.Value
			case "vcs.modified":
				info.Dirty = setting.Value != "false"
			}
		}
	}
	// A formal label alone must never turn unknown/dirty source into a clean release.
	if info.Kind == "formal" && (info.Dirty || len(info.Revision) != 40) {
		info.Kind = "development"
	}
	return info
}

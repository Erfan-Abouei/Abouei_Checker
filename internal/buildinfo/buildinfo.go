// Package buildinfo exposes version metadata injected at build time.
package buildinfo

import "runtime"

// These values are overridden with -ldflags "-X" by the build and release
// tooling. Their defaults identify a local development build.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// Info describes the running executable.
type Info struct {
	Version   string
	Commit    string
	BuildDate string
	GoVersion string
	Platform  string
}

// Current returns build information for the running executable.
func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

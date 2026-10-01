// Package version reports the build version of driveignore. Release builds
// override the variables below with -ldflags; other builds fall back to the
// module build information embedded by the Go toolchain.
package version

import (
	"fmt"
	"runtime/debug"
)

var (
	version = "devel"
	commit  = ""
	date    = ""
)

// String returns a human-readable version, for example
// "v1.2.0 (abc1234, 2026-10-01)" or "devel".
func String() string {
	v, c, d := version, commit, date
	if info, ok := debug.ReadBuildInfo(); ok {
		if v == "devel" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if c == "" {
					c = setting.Value
				}
			case "vcs.time":
				if d == "" {
					d = setting.Value
				}
			}
		}
	}
	return format(v, c, d)
}

// format renders version, commit and date, omitting empty parts.
func format(v, c, d string) string {
	if len(c) > 7 {
		c = c[:7]
	}
	switch {
	case c != "" && d != "":
		return fmt.Sprintf("%s (%s, %s)", v, c, d)
	case c != "":
		return fmt.Sprintf("%s (%s)", v, c)
	default:
		return v
	}
}

package version

import "runtime"

var (
	Version   = "0.3.0-dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

type Info struct {
	SchemaVersion int    `json:"schema_version"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	BuildDate     string `json:"build_date"`
	GoVersion     string `json:"go_version"`
}

func Current() Info {
	return Info{
		SchemaVersion: 1,
		Version:       Version,
		Commit:        Commit,
		BuildDate:     BuildDate,
		GoVersion:     runtime.Version(),
	}
}

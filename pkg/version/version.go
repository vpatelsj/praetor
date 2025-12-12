package version

var (
	// Version is set at build time via -ldflags if desired.
	Version = "v0.0.0"
	// Commit is the git SHA if provided at build time.
	Commit = ""
)

package version

// Value is the version string reported by --version and recorded in session headers. Release
// builds inject it with -ldflags -X; the default covers a plain go build or go install.
var Value = "dev"

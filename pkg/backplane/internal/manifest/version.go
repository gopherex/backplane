package manifest

// SDKVersion is the version of the backplane module linked into the binary,
// "(devel)" when unknown; the manifest's sdk_version.
func SDKVersion() string { return sdkVersion() }

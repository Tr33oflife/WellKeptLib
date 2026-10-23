package core

// linkfile.go — the hardlink creation seam (v1.1.0,
// LINK-LIBRARY-SPEC §10). Production link creation sits behind
// the package-level createHardlinkFn variable, mirroring
// extractEmbeddedSubtitleFn in probe.go: tests replace it
// (saving and restoring the original, never in parallel) to
// simulate link-time conditions — a cross-device EXDEV, an
// EEXIST race — that a one-filesystem test environment cannot
// produce for real. Link errors are classified ONCE, here,
// through the platform classifyLinkError, so Apply never
// scatters numeric errno checks.

// linkErrClass is the Apply-facing classification of a failed
// link creation (LINK-LIBRARY-SPEC §10.4).
type linkErrClass int

const (
	linkErrOther       linkErrClass = iota // surface the raw error as a failure
	linkErrCrossDevice                     // EXDEV — the pair is not on one filesystem after all
	linkErrExists                          // EEXIST — re-inspect the target and reclassify by identity
	linkErrUnsupported                     // the destination filesystem does not support hardlinks
)

// createHardlinkFn is the injectable link creator. Production
// value: createHardlink (platform files linkfile_unix.go /
// linkfile_windows.go).
var createHardlinkFn = createHardlink

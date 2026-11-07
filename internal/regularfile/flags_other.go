//go:build !unix

package regularfile

// Static and opened-handle type checks still apply. Unlike Unix, this fallback
// does not prevent blocking if a special object replaces the path before open.
const nonblock = 0

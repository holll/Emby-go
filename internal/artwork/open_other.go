//go:build !unix

package artwork

import "os/exec"

func containProcess(cmd *exec.Cmd) {}

// Pre/post-open identity checks still reject non-regular and replaced files on
// platforms without Unix FIFOs and O_NOFOLLOW.
const regularOpenFlags = 0

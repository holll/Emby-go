//go:build !unix

package probe

import "os/exec"

// Other platforms use CommandContext's direct-child kill. Descendant process
// termination is not guaranteed; WaitDelay still bounds inherited pipe draining.
func configureProcess(cmd *exec.Cmd) {}

func killProcess(cmd *exec.Cmd) error { return cmd.Process.Kill() }

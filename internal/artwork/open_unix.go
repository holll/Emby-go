//go:build unix

package artwork

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// A raced FIFO must not block before f.Stat can reject it. Never follow a
// replaced final symlink, even when it points to another file inside the root.
const regularOpenFlags = syscall.O_NONBLOCK | syscall.O_NOFOLLOW

func containProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Trusted executable paths can still be wrappers. Cancel the process
		// group so their decoder children cannot outlive a cancelled job.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

//go:build linux

package meeting

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Linux 容器取消下载时结束整个下载进程组，避免 FFmpeg 子进程继续写文件。
func configureDownloadProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 3 * time.Second
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
}

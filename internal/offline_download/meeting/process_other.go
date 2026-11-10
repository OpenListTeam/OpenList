//go:build !linux

package meeting

import "os/exec"

// 其他开发平台保留 exec.CommandContext 的默认取消行为。
func configureDownloadProcess(cmd *exec.Cmd) {}

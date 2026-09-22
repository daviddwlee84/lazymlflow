//go:build !unix

package brewupgrade

import (
	"os/exec"
	"time"
)

func configureCommand(cmd *exec.Cmd) { cmd.WaitDelay = 5 * time.Second }

package childprocess

import "os/exec"

func startReleased(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

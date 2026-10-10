//go:build !unix

package fix

import "os/exec"

func ownGroup(*exec.Cmd) {}

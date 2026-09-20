//go:build !windows

package main

import "os/exec"

func detachChild(*exec.Cmd) {}

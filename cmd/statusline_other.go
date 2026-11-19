//go:build !unix

package cmd

import "os/exec"

// ownGroup leaves c as it is: cancelling it kills the command alone.
func ownGroup(*exec.Cmd) {}

// killGroup does nothing: without process groups, only the command itself
// is stopped, and it has already exited or been killed.
func killGroup(*exec.Cmd) {}

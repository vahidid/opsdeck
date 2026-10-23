//go:build desktop && !darwin

package main

// loginShellEnv: apps started from a Linux desktop or the Windows Start menu
// already get the user's PATH.
func loginShellEnv() {}

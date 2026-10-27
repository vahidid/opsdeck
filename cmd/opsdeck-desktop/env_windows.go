//go:build desktop && windows

package main

// loginShellEnv: apps started from the Windows Start menu already get the
// user's PATH.
func loginShellEnv() {}

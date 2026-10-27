//go:build desktop && windows

package main

import _ "embed"

//go:embed build/tray.ico
var trayIcon []byte

// trayHost: Windows always has a notification area.
func trayHost() bool { return true }

//go:build desktop && darwin

package main

// startTray: on macOS opsdeck stays in the Dock instead. Closing the window
// hides the app (HideWindowOnClose) and clicking the Dock icon shows it; the
// tray library would take over the NSApplication delegate Wails needs.
func (a *app) startTray() bool { return false }

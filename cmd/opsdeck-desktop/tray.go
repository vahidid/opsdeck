//go:build desktop && (linux || windows)

package main

import (
	"log"
	"runtime"

	"fyne.io/systray"
)

// startTray puts opsdeck in the system tray: clicking it or Open shows the
// window, Quit quits gracefully. It reports whether a tray shows the icon;
// only then does closing the window hide it there.
func (a *app) startTray() bool {
	if !trayHost() {
		log.Printf("opsdeck: no system tray; closing the window quits")
		return false
	}
	go func() {
		// Windows: the tray's window and its message loop need one thread
		runtime.LockOSThread()
		systray.Run(func() {
			systray.SetIcon(trayIcon)
			systray.SetTitle("opsdeck")
			systray.SetTooltip("opsdeck")
			systray.SetOnTapped(a.focus)
			open := systray.AddMenuItem("Open opsdeck", "Show the opsdeck window")
			systray.AddSeparator()
			quit := systray.AddMenuItem("Quit opsdeck", "Disconnect VPNs and quit")
			for {
				select {
				case <-open.ClickedCh:
					a.focus()
				case <-quit.ClickedCh:
					a.quit()
				}
			}
		}, nil)
	}()
	return true
}

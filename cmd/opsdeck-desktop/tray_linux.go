//go:build desktop && linux

package main

import (
	_ "embed"

	"github.com/godbus/dbus/v5"
)

//go:embed build/tray.png
var trayIcon []byte

// trayHost reports whether the desktop shows tray icons: a
// StatusNotifierWatcher runs (KDE, Xfce, Cinnamon, GNOME with the
// AppIndicator extension, as on Ubuntu).
func trayHost() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}
	defer conn.Close()
	var has bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
}

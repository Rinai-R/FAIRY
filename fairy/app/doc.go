// Package app wires the FAIRY Wails 3 desktop shell.
//
// This package is the Go composition root: it creates Wails services, windows,
// tray menus, event emitters, shutdown hooks, and long-lived runtime handles.
// Domain packages such as companion, memory, model, config, speech, character,
// profile, and visual stay Wails-free and are constructed here.
//
// Keep product behavior in the domain packages. Add Wails-only concerns here
// when they are about process startup, windows, tray, lifecycle, or service
// registration.
package app

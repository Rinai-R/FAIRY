package main

import "github.com/wailsapp/wails/v3/pkg/application"

// windowRelation connects independent product windows at the native window
// server boundary. The relationship is established at lifecycle boundaries;
// pet movement must not call through this interface.
type windowRelation interface {
	Attach(parent, child application.Window) error
	Detach(parent, child application.Window) error
}

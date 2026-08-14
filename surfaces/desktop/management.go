package main

import (
	"errors"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	managementWidth     = 1280
	managementHeight    = 800
	managementMinWidth  = 960
	managementMinHeight = 640
)

func (s *CoreService) attachManagement(window application.Window) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.management = window
	s.managementOpen = false
}

func (s *CoreService) OpenManagement() error {
	s.mu.Lock()
	window := s.management
	if window == nil {
		s.mu.Unlock()
		return errors.New("management workspace is unavailable")
	}
	alreadyOpen := s.managementOpen
	s.managementOpen = true
	s.mu.Unlock()
	window.Show()
	window.Focus()
	if !alreadyOpen {
		s.emitManagementState(true)
	}
	return nil
}

func (s *CoreService) CloseManagement() error {
	s.mu.Lock()
	window := s.management
	s.managementOpen = false
	s.mu.Unlock()
	if window != nil {
		window.Hide()
	}
	s.emitManagementState(false)
	return nil
}

func (s *CoreService) emitManagementState(open bool) {
	s.mu.Lock()
	emit := s.emit
	s.mu.Unlock()
	if emit != nil {
		emit("desktop:management", map[string]bool{"open": open})
	}
}

func installApplicationMenu(app *application.App, core *CoreService) {
	if app == nil || core == nil {
		return
	}
	menu := application.DefaultApplicationMenu()
	workspace := menu.AddSubmenu("工作区")
	workspace.Add("打开管理工作区").OnClick(func(*application.Context) {
		_ = core.OpenManagement()
	})
	app.Menu.Set(menu)
}

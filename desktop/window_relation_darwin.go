//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -mmacosx-version-min=15.0 -x objective-c
#cgo LDFLAGS: -framework Cocoa

#import <AppKit/AppKit.h>

static int fairyAttachChildWindow(void* parentWindowPtr, void* childWindowPtr) {
	if (parentWindowPtr == NULL || childWindowPtr == NULL) {
		return 0;
	}

	__block int attached = 0;
	void (^attach)(void) = ^{
		NSWindow* parentWindow = (NSWindow*)parentWindowPtr;
		NSWindow* childWindow = (NSWindow*)childWindowPtr;
		NSWindow* currentParent = [childWindow parentWindow];
		if (currentParent != nil && currentParent != parentWindow) {
			[currentParent removeChildWindow:childWindow];
		}
		if ([childWindow parentWindow] != parentWindow) {
			[parentWindow addChildWindow:childWindow ordered:NSWindowAbove];
		}
		attached = [childWindow parentWindow] == parentWindow;
	};

	if ([NSThread isMainThread]) {
		attach();
	} else {
		dispatch_sync(dispatch_get_main_queue(), attach);
	}
	return attached;
}

static int fairyDetachChildWindow(void* parentWindowPtr, void* childWindowPtr) {
	if (parentWindowPtr == NULL || childWindowPtr == NULL) {
		return 0;
	}

	__block int detached = 0;
	void (^detach)(void) = ^{
		NSWindow* parentWindow = (NSWindow*)parentWindowPtr;
		NSWindow* childWindow = (NSWindow*)childWindowPtr;
		if ([childWindow parentWindow] == parentWindow) {
			[parentWindow removeChildWindow:childWindow];
		}
		detached = [childWindow parentWindow] != parentWindow;
	};

	if ([NSThread isMainThread]) {
		detach();
	} else {
		dispatch_sync(dispatch_get_main_queue(), detach);
	}
	return detached;
}
*/
import "C"

import (
	"errors"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type nativeWindowRelation struct{}

func newPlatformWindowRelation() windowRelation { return nativeWindowRelation{} }

func (nativeWindowRelation) Attach(parent, child application.Window) error {
	if parent == nil || child == nil {
		return errors.New("native window relation requires parent and child windows")
	}
	parentWindow, childWindow := parent.NativeWindow(), child.NativeWindow()
	if parentWindow == nil || childWindow == nil {
		return errors.New("native window relation is unavailable before window creation")
	}
	if C.fairyAttachChildWindow(parentWindow, childWindow) == 0 {
		return errors.New("attach native child window failed")
	}
	return nil
}

func (nativeWindowRelation) Detach(parent, child application.Window) error {
	if parent == nil || child == nil {
		return nil
	}
	parentWindow, childWindow := parent.NativeWindow(), child.NativeWindow()
	if parentWindow == nil || childWindow == nil {
		return nil
	}
	if C.fairyDetachChildWindow(parentWindow, childWindow) == 0 {
		return errors.New("detach native child window failed")
	}
	return nil
}

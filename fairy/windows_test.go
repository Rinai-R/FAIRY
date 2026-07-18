package main

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestMacCurrentSpaceCollectionBehaviorDoesNotJoinAllSpaces(t *testing.T) {
	if macCurrentSpaceCollectionBehavior&application.MacWindowCollectionBehaviorCanJoinAllSpaces != 0 {
		t.Fatal("product windows must stay on the current macOS Space, not join all Spaces")
	}
	if macCurrentSpaceCollectionBehavior&application.MacWindowCollectionBehaviorMoveToActiveSpace != 0 {
		t.Fatal("product windows must not move to whichever macOS Space becomes active")
	}
	if macCurrentSpaceCollectionBehavior&application.MacWindowCollectionBehaviorManaged == 0 {
		t.Fatal("product windows should use managed current-Space behavior")
	}
	if macCurrentSpaceCollectionBehavior&application.MacWindowCollectionBehaviorFullScreenNone == 0 {
		t.Fatal("product windows should not create or follow fullscreen Spaces")
	}
}

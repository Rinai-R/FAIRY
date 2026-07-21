//go:build integration

package main

import (
	"fmt"
	"testing"
	"time"
)

func TestMacOSKeychainRoundTrip(t *testing.T) {
	store := systemTokenStore{
		service: fmt.Sprintf("com.rinai.fairy.macos.test.%d", time.Now().UnixNano()),
		account: "temporary-roundtrip",
	}
	defer func() { _ = store.Delete() }()
	const token = "temporary-keychain-roundtrip-token"
	if err := store.Set(token); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get()
	if err != nil || got != token {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(); err == nil {
		t.Fatal("temporary Keychain item still exists after delete")
	}
}

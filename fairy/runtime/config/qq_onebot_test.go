//go:build !endpointstrict

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadQQOneBotSettingsMissingReturnsEmptyWithoutWriting(t *testing.T) {
	root := t.TempDir()
	settings, err := ReadQQOneBotSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if settings.SchemaVersion != qqOneBotSchemaVersion || settings.GroupAllowlist == nil || len(settings.GroupAllowlist) != 0 {
		t.Fatalf("settings = %#v", settings)
	}
	if _, err := os.Stat(filepath.Join(root, "qq_onebot")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("qq_onebot dir stat error = %v, want not exist", err)
	}
}

func TestWriteQQOneBotSettingsNormalizesDeduplicatesAndRoundTrips(t *testing.T) {
	root := t.TempDir()
	written, err := WriteQQOneBotSettings(root, QQOneBotSettings{
		GroupAllowlist: []string{" 00123 ", "456", "123", "000000000000000000000000456"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"123", "456"}
	if !reflect.DeepEqual(written.GroupAllowlist, want) {
		t.Fatalf("written allowlist = %#v, want %#v", written.GroupAllowlist, want)
	}
	read, err := ReadQQOneBotSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read, written) {
		t.Fatalf("read = %#v, want %#v", read, written)
	}
	info, err := os.Stat(filepath.Join(root, "qq_onebot", qqOneBotSettingsFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %o, want 600", info.Mode().Perm())
	}
}

func TestWriteQQOneBotSettingsAcceptsEmptyList(t *testing.T) {
	root := t.TempDir()
	if _, err := WriteQQOneBotSettings(root, QQOneBotSettings{GroupAllowlist: []string{"123"}}); err != nil {
		t.Fatal(err)
	}
	written, err := WriteQQOneBotSettings(root, QQOneBotSettings{GroupAllowlist: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if written.GroupAllowlist == nil || len(written.GroupAllowlist) != 0 {
		t.Fatalf("empty allowlist = %#v", written.GroupAllowlist)
	}
}

func TestWriteQQOneBotSettingsRejectsInvalidWithoutChangingPriorState(t *testing.T) {
	root := t.TempDir()
	want, err := WriteQQOneBotSettings(root, QQOneBotSettings{GroupAllowlist: []string{"123", "456"}})
	if err != nil {
		t.Fatal(err)
	}
	invalid := [][]string{
		{""}, {" "}, {"abc"}, {"-1"}, {"+1"}, {"0"}, {"1.5"}, {"18446744073709551616"},
	}
	for _, values := range invalid {
		if _, err := WriteQQOneBotSettings(root, QQOneBotSettings{GroupAllowlist: values}); err == nil {
			t.Fatalf("invalid allowlist %#v accepted", values)
		}
		got, err := ReadQQOneBotSettings(root)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("after %#v settings = %#v, want %#v", values, got, want)
		}
	}
}

func TestWriteQQOneBotSettingsRejectsMoreThanMaximumUniqueEntries(t *testing.T) {
	root := t.TempDir()
	if _, err := WriteQQOneBotSettings(root, QQOneBotSettings{GroupAllowlist: []string{"7"}}); err != nil {
		t.Fatal(err)
	}
	values := make([]string, MaxQQGroupAllowlist+1)
	for index := range values {
		values[index] = fmt.Sprint(index + 1)
	}
	if _, err := WriteQQOneBotSettings(root, QQOneBotSettings{GroupAllowlist: values}); err == nil {
		t.Fatal("oversized unique allowlist accepted")
	}
	got, err := ReadQQOneBotSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.GroupAllowlist, []string{"7"}) {
		t.Fatalf("allowlist after rejected write = %#v", got.GroupAllowlist)
	}
}

func TestReadQQOneBotSettingsRejectsCorruptOrUnsupportedDocument(t *testing.T) {
	for name, content := range map[string]string{
		"invalid JSON":       `{`,
		"unsupported schema": `{"schema_version":2,"data":{"schemaVersion":2,"groupAllowlist":[]}}`,
		"invalid stored ID":  `{"schema_version":1,"data":{"schemaVersion":1,"groupAllowlist":["-1"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "qq_onebot")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, qqOneBotSettingsFile), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadQQOneBotSettings(root); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
}

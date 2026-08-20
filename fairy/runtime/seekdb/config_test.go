package seekdb

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigFromEnv(t *testing.T) {
	library := filepath.Join(t.TempDir(), "libseekdb.dylib")
	values := map[string]string{
		EnvLibrary:       library,
		EnvDataDir:       filepath.Join(t.TempDir(), "data"),
		EnvDatabase:      "fairy_test",
		EnvConnectLimit:  "1500ms",
		EnvStartLimit:    "2s",
		EnvQueryLimit:    "3s",
		EnvShutdownLimit: "4s",
		EnvMaxOpenConns:  "12",
		EnvMaxIdleConns:  "6",
	}
	config, err := ConfigFromEnv(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if config.LibraryPath != library || config.Database != "fairy_test" {
		t.Fatalf("ConfigFromEnv() = %+v", config)
	}
	if config.ConnectLimit != 1500*time.Millisecond || config.StartLimit != 2*time.Second || config.QueryLimit != 3*time.Second || config.ShutdownLimit != 4*time.Second {
		t.Fatalf("ConfigFromEnv() limits = %+v", config)
	}
	if config.MaxOpenConns != 12 || config.MaxIdleConns != 6 {
		t.Fatalf("ConfigFromEnv() pool = %+v", config)
	}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	values := map[string]string{
		EnvDataDir: filepath.Join(t.TempDir(), "data"),
	}
	config, err := ConfigFromEnv(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if config.Database != DefaultDatabase {
		t.Fatalf("ConfigFromEnv() defaults = %+v", config)
	}
	if config.ConnectLimit != DefaultConnectLimit || config.StartLimit != DefaultStartLimit || config.QueryLimit != DefaultQueryLimit || config.ShutdownLimit != DefaultShutdownLimit {
		t.Fatalf("ConfigFromEnv() default limits = %+v", config)
	}
	if config.MaxOpenConns != DefaultMaxOpenConns || config.MaxIdleConns != DefaultMaxIdleConns {
		t.Fatalf("ConfigFromEnv() default pool = %+v", config)
	}
}

func TestConfigValidationRejectsUnsafeValues(t *testing.T) {
	valid := Config{
		LibraryPath:  filepath.Join(t.TempDir(), "libseekdb.dylib"),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		Database:     DefaultDatabase,
		ConnectLimit: DefaultConnectLimit, StartLimit: DefaultStartLimit, QueryLimit: DefaultQueryLimit, ShutdownLimit: DefaultShutdownLimit,
		MaxOpenConns: DefaultMaxOpenConns, MaxIdleConns: DefaultMaxIdleConns,
	}
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "relative library", mutate: func(c *Config) { c.LibraryPath = "libseekdb.dylib" }, want: "must be absolute"},
		{name: "unclean data", mutate: func(c *Config) { c.DataDir += string(filepath.Separator) + ".." }, want: "must be clean"},
		{name: "root data", mutate: func(c *Config) { c.DataDir = string(filepath.Separator) }, want: "filesystem root"},
		{name: "unsafe database", mutate: func(c *Config) { c.Database = "fairy-test" }, want: "portable SQL identifier"},
		{name: "surrounding whitespace", mutate: func(c *Config) { c.DataDir = " " + c.DataDir }, want: ErrUncleanValue.Error()},
		{name: "zero timeout", mutate: func(c *Config) { c.QueryLimit = 0 }, want: "greater than zero"},
		{name: "too many connections", mutate: func(c *Config) { c.MaxOpenConns = maximumOpenConns + 1 }, want: "between 1"},
		{name: "idle exceeds open", mutate: func(c *Config) { c.MaxIdleConns = c.MaxOpenConns + 1 }, want: "between 0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestConfigFromEnvPreservesRequiredErrors(t *testing.T) {
	_, err := ConfigFromEnv(func(string) string { return "" })
	if !errors.Is(err, ErrDataDirRequired) {
		t.Fatalf("ConfigFromEnv() error = %v", err)
	}
}

func TestConfigFromEnvRejectsDeprecatedProcessEnvironment(t *testing.T) {
	values := map[string]string{
		EnvDataDir:    filepath.Join(t.TempDir(), "data"),
		EnvBinaryPath: "/usr/bin/seekdb",
	}
	_, err := ConfigFromEnv(func(name string) string { return values[name] })
	if !errors.Is(err, ErrDeprecatedProcessEnv) {
		t.Fatalf("ConfigFromEnv() error = %v", err)
	}
}

func TestProfileGetenvDerivesDataDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profile")
	getenv := ProfileGetenv(root, func(string) string { return "" })
	got := getenv(EnvDataDir)
	if got != filepath.Join(root, "seekdb") {
		t.Fatalf("ProfileGetenv() data dir = %q", got)
	}
}

func TestDescriptorDoesNotExposePaths(t *testing.T) {
	secretRoot := filepath.Join(t.TempDir(), "private-user-path")
	config := Config{LibraryPath: filepath.Join(secretRoot, "libseekdb.dylib"), DataDir: filepath.Join(secretRoot, "data"), Database: DefaultDatabase}
	descriptor := config.Descriptor()
	if descriptor.Engine != "libseekdb.dylib" || descriptor.Database != DefaultDatabase {
		t.Fatalf("Descriptor() = %+v", descriptor)
	}
	if strings.Contains(descriptor.Engine, secretRoot) {
		t.Fatalf("Descriptor() leaked library path: %+v", descriptor)
	}
}

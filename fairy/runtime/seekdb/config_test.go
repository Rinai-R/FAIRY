package seekdb

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigFromEnv(t *testing.T) {
	values := map[string]string{
		EnvBinaryPath:    filepath.Join(t.TempDir(), "seekdb"),
		EnvDataDir:       filepath.Join(t.TempDir(), "data"),
		EnvAddress:       "[::1]:3881",
		EnvDatabase:      "fairy_test",
		EnvUser:          "fairy_runtime",
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
	if config.Address != "[::1]:3881" || config.Database != "fairy_test" || config.User != "fairy_runtime" {
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
		EnvBinaryPath: filepath.Join(t.TempDir(), "seekdb"),
		EnvDataDir:    filepath.Join(t.TempDir(), "data"),
	}
	config, err := ConfigFromEnv(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if config.Address != DefaultAddress || config.Database != DefaultDatabase || config.User != DefaultUser {
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
		BinaryPath: filepath.Join(t.TempDir(), "seekdb"),
		DataDir:    filepath.Join(t.TempDir(), "data"), Address: DefaultAddress,
		Database: DefaultDatabase, User: DefaultUser,
		ConnectLimit: DefaultConnectLimit, StartLimit: DefaultStartLimit, QueryLimit: DefaultQueryLimit, ShutdownLimit: DefaultShutdownLimit,
		MaxOpenConns: DefaultMaxOpenConns, MaxIdleConns: DefaultMaxIdleConns,
	}
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "missing binary", mutate: func(c *Config) { c.BinaryPath = "" }, want: ErrBinaryPathRequired.Error()},
		{name: "relative binary", mutate: func(c *Config) { c.BinaryPath = "seekdb" }, want: "must be absolute"},
		{name: "unclean data", mutate: func(c *Config) { c.DataDir += string(filepath.Separator) + ".." }, want: "must be clean"},
		{name: "root data", mutate: func(c *Config) { c.DataDir = string(filepath.Separator) }, want: "filesystem root"},
		{name: "remote address", mutate: func(c *Config) { c.Address = "192.0.2.1:2881" }, want: "loopback"},
		{name: "hostname", mutate: func(c *Config) { c.Address = "localhost:2881" }, want: "IP literal"},
		{name: "zero port", mutate: func(c *Config) { c.Address = "127.0.0.1:0" }, want: "between 1 and 65535"},
		{name: "unsafe database", mutate: func(c *Config) { c.Database = "fairy-test" }, want: "portable SQL identifier"},
		{name: "unsafe user", mutate: func(c *Config) { c.User = "root@test" }, want: "portable SQL identifier"},
		{name: "surrounding whitespace", mutate: func(c *Config) { c.Address = " " + c.Address }, want: ErrUncleanValue.Error()},
		{name: "zero timeout", mutate: func(c *Config) { c.QueryLimit = 0 }, want: "greater than zero"},
		{name: "too many connections", mutate: func(c *Config) { c.MaxOpenConns = maximumOpenConns + 1 }, want: "between 1"},
		{name: "idle exceeds open", mutate: func(c *Config) { c.MaxIdleConns = c.MaxOpenConns + 1 }, want: "between 0"},
		{name: "password NUL", mutate: func(c *Config) { c.Password = "bad\x00password" }, want: "must not contain NUL"},
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
	if !errors.Is(err, ErrBinaryPathRequired) {
		t.Fatalf("ConfigFromEnv() error = %v", err)
	}
}

func TestDescriptorDoesNotExposePaths(t *testing.T) {
	secretRoot := filepath.Join(t.TempDir(), "private-user-path")
	config := Config{BinaryPath: filepath.Join(secretRoot, "seekdb"), DataDir: filepath.Join(secretRoot, "data"), Address: DefaultAddress, Database: DefaultDatabase}
	descriptor := config.Descriptor()
	if descriptor.BinaryName != "seekdb" || descriptor.Address != DefaultAddress || descriptor.Database != DefaultDatabase {
		t.Fatalf("Descriptor() = %+v", descriptor)
	}
	if strings.Contains(descriptor.BinaryName, secretRoot) {
		t.Fatalf("Descriptor() leaked binary path: %+v", descriptor)
	}
}

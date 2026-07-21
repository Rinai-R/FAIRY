package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	CoreEndpoint          string
	CoreToken             string
	OneBotWebhookEndpoint string
	OneBotAPIEndpoint     string
	OneBotToken           string
	GroupAllowlist        []string
}

func (c Config) Validate() error {
	if c.CoreToken == "" || c.OneBotToken == "" {
		return errors.New("Core and OneBot tokens are required")
	}
	if err := validateEndpoint(c.CoreEndpoint, "https", "http", "Core"); err != nil {
		return err
	}
	if err := validateLocalHTTPEndpoint(c.OneBotWebhookEndpoint, "OneBot webhook"); err != nil {
		return err
	}
	if err := validateLocalHTTPEndpoint(c.OneBotAPIEndpoint, "OneBot API"); err != nil {
		return err
	}
	if len(c.GroupAllowlist) == 0 {
		return errors.New("OneBot group allowlist must be non-empty")
	}
	for _, group := range c.GroupAllowlist {
		if strings.TrimSpace(group) != group {
			return errors.New("OneBot group allowlist contains invalid entry")
		}
		if _, err := positiveID(group, "OneBot group allowlist entry"); err != nil {
			return err
		}
	}
	return nil
}

func validateLocalHTTPEndpoint(raw, label string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || !isLoopback(parsed.Hostname()) || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("%s endpoint must be a loopback http URL without userinfo, query, fragment or path", label)
	}
	return nil
}

func positiveID(raw, label string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", label)
	}
	return id, nil
}

func validateEndpoint(raw, remoteScheme, localScheme, label string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("%s endpoint is invalid", label)
	}
	if parsed.Scheme == remoteScheme {
		return nil
	}
	if parsed.Scheme != localScheme || !isLoopback(parsed.Hostname()) {
		return fmt.Errorf("remote %s endpoint requires %s; %s is allowed only for loopback", label, remoteScheme, localScheme)
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func configFromEnv() (Config, error) {
	v := viper.New()
	keys := []string{"FAIRY_CORE_ENDPOINT", "FAIRY_CORE_TOKEN", "FAIRY_ONEBOT_WEBHOOK_ENDPOINT", "FAIRY_ONEBOT_API_ENDPOINT", "FAIRY_ONEBOT_TOKEN", "FAIRY_ONEBOT_GROUP_ALLOWLIST"}
	for _, key := range keys {
		if err := v.BindEnv(key); err != nil {
			return Config{}, fmt.Errorf("bind %s: %w", key, err)
		}
	}
	read := func(key string) string { return strings.TrimSpace(v.GetString(key)) }
	cfg := Config{
		CoreEndpoint: read("FAIRY_CORE_ENDPOINT"), CoreToken: v.GetString("FAIRY_CORE_TOKEN"),
		OneBotWebhookEndpoint: read("FAIRY_ONEBOT_WEBHOOK_ENDPOINT"), OneBotAPIEndpoint: read("FAIRY_ONEBOT_API_ENDPOINT"),
		OneBotToken: v.GetString("FAIRY_ONEBOT_TOKEN"), GroupAllowlist: splitAllowlist(read("FAIRY_ONEBOT_GROUP_ALLOWLIST")),
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

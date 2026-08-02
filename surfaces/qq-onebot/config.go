package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	CoreEndpoint          string
	CoreToken             string
	OneBotWebhookEndpoint string
	OneBotAPIEndpoint     string
	OneBotToken           string
	ContainerNetwork      bool
}

func (c Config) Validate() error {
	if c.CoreToken == "" || c.OneBotToken == "" {
		return errors.New("Core and OneBot tokens are required")
	}
	if err := validateCoreEndpoint(c.CoreEndpoint, c.ContainerNetwork); err != nil {
		return err
	}
	if err := validateOneBotHTTPEndpoint(c.OneBotWebhookEndpoint, "OneBot webhook", c.ContainerNetwork, true); err != nil {
		return err
	}
	if err := validateOneBotHTTPEndpoint(c.OneBotAPIEndpoint, "OneBot API", c.ContainerNetwork, false); err != nil {
		return err
	}
	return nil
}

func validateOneBotHTTPEndpoint(raw, label string, containerNetwork, listener bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || invalidEndpointShape(parsed) {
		return fmt.Errorf("%s endpoint must be an authorized http URL without userinfo, query, fragment or path", label)
	}
	host := parsed.Hostname()
	allowed := isLoopback(host)
	if containerNetwork {
		allowed = allowed || isContainerServiceHost(host) || (listener && host == "0.0.0.0")
	}
	if !allowed {
		return fmt.Errorf("%s endpoint host is not authorized", label)
	}
	return nil
}

func validateCoreEndpoint(raw string, containerNetwork bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || invalidEndpointShape(parsed) {
		return errors.New("Core endpoint is invalid")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" || (!isLoopback(parsed.Hostname()) && !(containerNetwork && isContainerServiceHost(parsed.Hostname()))) {
		return errors.New("remote Core endpoint requires https; http is allowed only for loopback or explicit container network services")
	}
	return nil
}

func invalidEndpointShape(parsed *url.URL) bool {
	return parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/")
}

func isContainerServiceHost(host string) bool {
	if host == "" || len(host) > 63 || host != strings.ToLower(host) || net.ParseIP(host) != nil {
		return false
	}
	for i, r := range host {
		alphanumeric := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if !alphanumeric && (r != '-' || i == 0 || i == len(host)-1) {
			return false
		}
	}
	return true
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
	keys := []string{"FAIRY_CORE_ENDPOINT", "FAIRY_CORE_TOKEN", "FAIRY_ONEBOT_WEBHOOK_ENDPOINT", "FAIRY_ONEBOT_API_ENDPOINT", "FAIRY_ONEBOT_TOKEN", "FAIRY_ONEBOT_CONTAINER_NETWORK"}
	for _, key := range keys {
		if err := v.BindEnv(key); err != nil {
			return Config{}, fmt.Errorf("bind %s: %w", key, err)
		}
	}
	read := func(key string) string { return strings.TrimSpace(v.GetString(key)) }
	containerNetwork, err := parseContainerNetwork(read("FAIRY_ONEBOT_CONTAINER_NETWORK"))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		CoreEndpoint: read("FAIRY_CORE_ENDPOINT"), CoreToken: v.GetString("FAIRY_CORE_TOKEN"),
		OneBotWebhookEndpoint: read("FAIRY_ONEBOT_WEBHOOK_ENDPOINT"), OneBotAPIEndpoint: read("FAIRY_ONEBOT_API_ENDPOINT"),
		OneBotToken:      v.GetString("FAIRY_ONEBOT_TOKEN"),
		ContainerNetwork: containerNetwork,
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func parseContainerNetwork(raw string) (bool, error) {
	switch raw {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, errors.New("FAIRY_ONEBOT_CONTAINER_NETWORK must be true or false")
	}
}

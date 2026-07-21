package bridge

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type Config struct {
	CoreEndpoint   string
	CoreToken      string
	OneBotEndpoint string
	OneBotToken    string
	SelfID         string
	GroupAllowlist []string
}

func (c Config) Validate() error {
	if c.CoreToken == "" || c.OneBotToken == "" {
		return errors.New("Core and OneBot tokens are required from exact environment variables")
	}
	if err := validateEndpoint(c.CoreEndpoint, "https", "http", "Core"); err != nil {
		return err
	}
	if err := validateEndpoint(c.OneBotEndpoint, "wss", "ws", "OneBot"); err != nil {
		return err
	}
	if strings.TrimSpace(c.SelfID) == "" {
		return errors.New("OneBot self ID is required")
	}
	if selfID, err := strconv.ParseInt(c.SelfID, 10, 64); err != nil || selfID <= 0 {
		return errors.New("OneBot self ID must be a positive integer")
	}
	if len(c.GroupAllowlist) == 0 {
		return errors.New("OneBot group allowlist must be non-empty")
	}
	for _, group := range c.GroupAllowlist {
		if strings.TrimSpace(group) == "" || strings.TrimSpace(group) != group {
			return errors.New("OneBot group allowlist contains invalid entry")
		}
		if groupID, err := strconv.ParseInt(group, 10, 64); err != nil || groupID <= 0 {
			return errors.New("OneBot group allowlist entries must be positive integers")
		}
	}
	return nil
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

package qqonebot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const MaxGroupAllowlist = 256

type InstanceConfig struct {
	SchemaVersion  uint32   `json:"schemaVersion"`
	GroupAllowlist []string `json:"groupAllowlist"`
	APIBaseURL     string   `json:"apiBaseURL,omitempty"`
	IngressBind    string   `json:"ingressBind,omitempty"`
}

func NormalizeAllowlist(values []string) ([]string, error) {
	normalized := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, errors.New("QQ group number must be a positive decimal integer")
		}
		for _, character := range value {
			if character < '0' || character > '9' {
				return nil, fmt.Errorf("QQ group number %q must be a positive decimal integer", value)
			}
		}
		id, err := strconv.ParseUint(value, 10, 64)
		if err != nil || id == 0 {
			return nil, fmt.Errorf("QQ group number %q must be a positive decimal integer within uint64 range", value)
		}
		canonical := strconv.FormatUint(id, 10)
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
		if len(normalized) > MaxGroupAllowlist {
			return nil, fmt.Errorf("QQ group allowlist exceeds %d unique entries", MaxGroupAllowlist)
		}
	}
	if normalized == nil {
		normalized = []string{}
	}
	return normalized, nil
}

func GroupAllowed(allowlist []string, groupID string) (bool, error) {
	normalized, err := NormalizeAllowlist(allowlist)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(groupID) == "" {
		return false, nil
	}
	for _, allowed := range normalized {
		if allowed == groupID {
			return true, nil
		}
	}
	return false, nil
}

func ParseInstanceConfig(raw json.RawMessage) (InstanceConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return InstanceConfig{SchemaVersion: 1, GroupAllowlist: []string{}}, nil
	}
	var config InstanceConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return InstanceConfig{}, errors.New("QQ plugin config is invalid")
	}
	allowlist, err := NormalizeAllowlist(config.GroupAllowlist)
	if err != nil {
		return InstanceConfig{}, err
	}
	config.SchemaVersion = 1
	config.GroupAllowlist = allowlist
	config.APIBaseURL = strings.TrimRight(strings.TrimSpace(config.APIBaseURL), "/")
	config.IngressBind = strings.TrimSpace(config.IngressBind)
	return config, nil
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

type coreConfigReader interface {
	GetConfig(context.Context, string) (json.RawMessage, error)
}

type coreGroupAuthorizer struct {
	config coreConfigReader
}

func (authorizer coreGroupAuthorizer) GroupAllowed(ctx context.Context, groupID int64) (bool, error) {
	if authorizer.config == nil {
		return false, errors.New("Core QQ allowlist reader is unavailable")
	}
	if groupID <= 0 {
		return false, nil
	}
	raw, err := authorizer.config.GetConfig(ctx, "qq-onebot")
	if err != nil {
		return false, err
	}
	var settings struct {
		SchemaVersion  uint32   `json:"schemaVersion"`
		GroupAllowlist []string `json:"groupAllowlist"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return false, fmt.Errorf("parsing Core QQ allowlist: %w", err)
	}
	if settings.SchemaVersion != 1 || settings.GroupAllowlist == nil {
		return false, errors.New("Core QQ allowlist response is missing required fields")
	}
	wanted := strconv.FormatInt(groupID, 10)
	for _, group := range settings.GroupAllowlist {
		if !canonicalPositiveDecimal(group) {
			return false, errors.New("Core QQ allowlist response contains an invalid group number")
		}
		if group == wanted {
			return true, nil
		}
	}
	return false, nil
}

func canonicalPositiveDecimal(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

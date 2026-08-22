//go:build !endpointstrict

package edge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"fairy/app/core"
	"fairy/plugin"
	"fairy/plugin/qqonebot"
)

var ErrQQDisabledByProfile = errors.New("QQ/OneBot is disabled by the endpoint-strict profile")

type QQSettings struct {
	SchemaVersion  uint32   `json:"schemaVersion"`
	GroupAllowlist []string `json:"groupAllowlist"`
	InstanceID     string   `json:"instanceId,omitempty"`
	Ready          bool     `json:"ready"`
	APIBaseURL     string   `json:"apiBaseURL,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

func (m *Management) QQ() (QQSettings, error) {
	if m == nil || m.runtime == nil {
		return QQSettings{}, ErrManagementUnavailable
	}
	if rt := m.coreRuntime(); rt != nil && rt.RuntimeProfile == core.ProfileEndpointStrict {
		return QQSettings{
			SchemaVersion:  1,
			GroupAllowlist: []string{},
			Reason:         "disabled_by_endpoint_strict",
		}, nil
	}
	if m.runtime.plugins == nil {
		return QQSettings{SchemaVersion: 1, GroupAllowlist: []string{}}, nil
	}
	records, err := m.runtime.plugins.Instances(context.Background())
	if err != nil {
		return QQSettings{}, err
	}
	record, ok := qqPluginInstance(records)
	if !ok {
		return QQSettings{SchemaVersion: 1, GroupAllowlist: []string{}}, nil
	}
	config, err := qqonebot.ParseInstanceConfig(record.ConfigDocument)
	if err != nil {
		return QQSettings{}, err
	}
	_, discovered := qqonebot.Discover([]plugin.InstanceRecord{record})
	return QQSettings{
		SchemaVersion:  config.SchemaVersion,
		GroupAllowlist: config.GroupAllowlist,
		InstanceID:     record.ID,
		Ready:          discovered,
		APIBaseURL:     config.APIBaseURL,
	}, nil
}

func (m *Management) SaveQQ(settings QQSettings) (QQSettings, error) {
	if m == nil || m.runtime == nil {
		return QQSettings{}, ErrManagementUnavailable
	}
	if rt := m.coreRuntime(); rt != nil && rt.RuntimeProfile == core.ProfileEndpointStrict {
		return QQSettings{}, ErrQQDisabledByProfile
	}
	if m.runtime.plugins == nil {
		return QQSettings{}, ErrQQPluginNotInstalled
	}
	records, err := m.runtime.plugins.Instances(context.Background())
	if err != nil {
		return QQSettings{}, err
	}
	record, ok := qqPluginInstance(records)
	if !ok {
		return QQSettings{}, ErrQQPluginNotInstalled
	}
	current, err := qqonebot.ParseInstanceConfig(record.ConfigDocument)
	if err != nil {
		return QQSettings{}, err
	}
	allowlist, err := qqonebot.NormalizeAllowlist(settings.GroupAllowlist)
	if err != nil {
		return QQSettings{}, err
	}
	current.GroupAllowlist = allowlist
	if strings.TrimSpace(settings.APIBaseURL) != "" {
		current.APIBaseURL = strings.TrimRight(strings.TrimSpace(settings.APIBaseURL), "/")
	}
	current.SchemaVersion = 1
	raw, err := json.Marshal(current)
	if err != nil {
		return QQSettings{}, err
	}
	record.ConfigDocument = raw
	if err := m.runtime.plugins.PutInstance(context.Background(), record); err != nil {
		return QQSettings{}, err
	}
	return m.QQ()
}

func qqPluginInstance(records []plugin.InstanceRecord) (plugin.InstanceRecord, bool) {
	for _, record := range records {
		if record.PluginID == qqonebot.PluginID {
			return record, true
		}
	}
	return plugin.InstanceRecord{}, false
}

//go:build !endpointstrict

package edge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fairy/app/core"
	"fairy/plugin"
	"fairy/plugin/qqonebot"
)

func TestEndpointStrictProjectsQQConflictWithoutReadingStoredPluginConfig(t *testing.T) {
	management := &Management{runtime: &Runtime{core: &core.Runtime{RuntimeProfile: core.ProfileEndpointStrict}}}

	status, err := management.QQ()
	if err != nil {
		t.Fatal(err)
	}
	if status.Ready || status.APIBaseURL != "" || status.InstanceID != "" ||
		status.Reason != "disabled_by_endpoint_strict" || status.GroupAllowlist == nil {
		t.Fatalf("endpoint-strict QQ status = %#v", status)
	}
	if _, err := management.SaveQQ(QQSettings{APIBaseURL: "http://127.0.0.1:3000"}); !errors.Is(err, ErrQQDisabledByProfile) {
		t.Fatalf("SaveQQ() error = %v, want %v", err, ErrQQDisabledByProfile)
	}
}

func TestQQProfileSwitchPreservesExplicitNonStrictState(t *testing.T) {
	store := &recordingPluginStore{instances: []plugin.InstanceRecord{{
		ID: "qq-dev", PluginID: qqonebot.PluginID, PluginVersion: "1.0.0",
		Enabled: true, Lifecycle: "ready",
		CapabilityGrants: []string{"http.request", "event.emit", "action.complete"},
		ConfigDocument:   json.RawMessage(`{"schemaVersion":1,"groupAllowlist":["123"],"apiBaseURL":"http://127.0.0.1:3000"}`),
	}}}
	strict := &Management{runtime: &Runtime{
		core: &core.Runtime{RuntimeProfile: core.ProfileEndpointStrict}, plugins: store,
	}}
	status, err := strict.QQ()
	if err != nil || status.Reason != "disabled_by_endpoint_strict" || status.Ready {
		t.Fatalf("strict QQ() = (%#v, %v)", status, err)
	}
	if store.instanceReads != 0 || store.instanceWrites != 0 {
		t.Fatalf("strict profile touched QQ state: reads=%d writes=%d", store.instanceReads, store.instanceWrites)
	}
	if _, err := strict.SaveQQ(QQSettings{GroupAllowlist: []string{"456"}}); !errors.Is(err, ErrQQDisabledByProfile) {
		t.Fatalf("strict SaveQQ() = %v", err)
	}
	if store.instanceReads != 0 || store.instanceWrites != 0 {
		t.Fatalf("strict save touched QQ state: reads=%d writes=%d", store.instanceReads, store.instanceWrites)
	}

	full := &Management{runtime: &Runtime{
		core: &core.Runtime{RuntimeProfile: core.ProfileFull}, plugins: store,
	}}
	status, err = full.QQ()
	if err != nil || !status.Ready || status.InstanceID != "qq-dev" || status.APIBaseURL != "http://127.0.0.1:3000" || len(status.GroupAllowlist) != 1 || status.GroupAllowlist[0] != "123" {
		t.Fatalf("full QQ() after profile switch = (%#v, %v)", status, err)
	}
	status, err = full.SaveQQ(QQSettings{GroupAllowlist: []string{"00456"}, APIBaseURL: "http://127.0.0.1:3001/"})
	if err != nil || !status.Ready || status.APIBaseURL != "http://127.0.0.1:3001" || len(status.GroupAllowlist) != 1 || status.GroupAllowlist[0] != "456" {
		t.Fatalf("full SaveQQ() = (%#v, %v)", status, err)
	}
	if store.instanceWrites != 1 {
		t.Fatalf("full profile writes = %d, want 1", store.instanceWrites)
	}
}

func TestEndpointStrictQQErrorProjectionDoesNotReadNonStrictFailure(t *testing.T) {
	storedFailure := errors.New("onebot credential backend failed: secret-must-not-project")
	store := &recordingPluginStore{instancesErr: storedFailure}
	strict := &Management{runtime: &Runtime{
		core: &core.Runtime{RuntimeProfile: core.ProfileEndpointStrict}, plugins: store,
	}}
	status, err := strict.QQ()
	if err != nil || status.Reason != "disabled_by_endpoint_strict" || strings.Contains(strings.ToLower(status.Reason), "secret") {
		t.Fatalf("strict QQ error projection = (%#v, %v)", status, err)
	}
	if store.instanceReads != 0 {
		t.Fatalf("strict profile queried failing QQ store %d times", store.instanceReads)
	}

	full := &Management{runtime: &Runtime{
		core: &core.Runtime{RuntimeProfile: core.ProfileFull}, plugins: store,
	}}
	if _, err := full.QQ(); !errors.Is(err, storedFailure) {
		t.Fatalf("full QQ() = %v, want explicit extension error", err)
	}
}

type recordingPluginStore struct {
	instances      []plugin.InstanceRecord
	instancesErr   error
	instanceReads  int
	instanceWrites int
}

func (s *recordingPluginStore) Instances(context.Context) ([]plugin.InstanceRecord, error) {
	s.instanceReads++
	if s.instancesErr != nil {
		return nil, s.instancesErr
	}
	return append([]plugin.InstanceRecord(nil), s.instances...), nil
}

func (s *recordingPluginStore) Upgrades(context.Context, string) ([]plugin.UpgradeRecord, error) {
	return []plugin.UpgradeRecord{}, nil
}

func (s *recordingPluginStore) PutInstance(_ context.Context, record plugin.InstanceRecord) error {
	s.instanceWrites++
	for index := range s.instances {
		if s.instances[index].ID == record.ID {
			s.instances[index] = record
			return nil
		}
	}
	s.instances = append(s.instances, record)
	return nil
}

func (s *recordingPluginStore) ConfigRefs(context.Context, string) ([]plugin.ConfigRef, error) {
	return []plugin.ConfigRef{}, nil
}

package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"fairy/plugin"
)

var ErrInstallerStoreRequired = errors.New("plugin installer store is required")

type Installer struct {
	host  *Host
	store *plugin.Store
	now   func() time.Time
}

func NewInstaller(host *Host, store *plugin.Store) (*Installer, error) {
	if host == nil {
		return nil, ErrHostClosed
	}
	if store == nil {
		return nil, ErrInstallerStoreRequired
	}
	return &Installer{host: host, store: store, now: time.Now}, nil
}

func (h *Host) ShadowHealth(ctx context.Context, name string, binary []byte) error {
	instance, err := h.LoadGranted(ctx, name, binary, DefaultBudget(), Grant{})
	if err != nil {
		return fmt.Errorf("plugin shadow health: %w", err)
	}
	defer instance.Close(ctx)
	envelope, err := plugin.EncodeEnvelope(plugin.Envelope{
		ABIVersion:  plugin.ABIVersion,
		Kind:        "init",
		Correlation: plugin.Correlation{PluginInstanceID: name},
	})
	if err != nil {
		return fmt.Errorf("plugin shadow health: %w", err)
	}
	if _, err := instance.Init(ctx, envelope); err != nil {
		return fmt.Errorf("plugin shadow health: %w", err)
	}
	if err := instance.Shutdown(ctx); err != nil {
		return fmt.Errorf("plugin shadow health: %w", err)
	}
	return nil
}

func (i *Installer) Install(ctx context.Context, instanceID string, bundle plugin.Bundle) error {
	if err := i.host.ShadowHealth(ctx, instanceID+"-shadow", bundle.Module); err != nil {
		return err
	}
	if err := i.store.PutPackage(ctx, packageRecord(bundle, i.unixMS())); err != nil {
		return err
	}
	return i.store.PutInstance(ctx, plugin.InstanceRecord{
		ID:             instanceID,
		PluginID:       bundle.Manifest.ID,
		PluginVersion:  bundle.Manifest.Version,
		Enabled:        false,
		Lifecycle:      "disabled",
		ConfigDocument: json.RawMessage(`{}`),
	})
}

func (i *Installer) Enable(ctx context.Context, instanceID string, bundle plugin.Bundle) error {
	current, err := i.store.Instance(ctx, instanceID)
	if err != nil {
		return err
	}
	if err := i.host.ShadowHealth(ctx, instanceID+"-shadow", bundle.Module); err != nil {
		return err
	}
	current.Enabled = true
	current.Lifecycle = "ready"
	return i.store.PutInstance(ctx, current)
}

func (i *Installer) Disable(ctx context.Context, instanceID string) error {
	current, err := i.store.Instance(ctx, instanceID)
	if err != nil {
		return err
	}
	current.Enabled = false
	current.Lifecycle = "disabled"
	return i.store.PutInstance(ctx, current)
}

func (i *Installer) Upgrade(ctx context.Context, instanceID string, bundle plugin.Bundle) error {
	current, err := i.store.Instance(ctx, instanceID)
	if err != nil {
		return err
	}
	from := current.PluginVersion
	started := i.unixMS()
	journalID := instanceID + "-upgrade-" + fmt.Sprint(started)
	if err := i.store.AppendUpgrade(ctx, plugin.UpgradeRecord{
		JournalID: journalID, InstanceID: instanceID,
		FromVersion: from, ToVersion: bundle.Manifest.Version,
		Status: "started", StartedAtUnixMS: started,
	}); err != nil {
		return err
	}
	if err := i.host.ShadowHealth(ctx, instanceID+"-shadow", bundle.Module); err != nil {
		_ = i.store.AppendUpgrade(ctx, plugin.UpgradeRecord{
			JournalID: journalID + "-rb", InstanceID: instanceID,
			FromVersion: from, ToVersion: bundle.Manifest.Version,
			Status: "rolled_back", ErrorCode: plugin.CodeManifestInvalid, ErrorMessage: "shadow health failed",
			StartedAtUnixMS: started, FinishedAtUnixMS: i.unixMS(),
		})
		return err
	}
	if err := i.store.PutPackage(ctx, packageRecord(bundle, i.unixMS())); err != nil {
		return err
	}
	current.PluginVersion = bundle.Manifest.Version
	if err := i.store.PutInstance(ctx, current); err != nil {
		return err
	}
	return i.store.AppendUpgrade(ctx, plugin.UpgradeRecord{
		JournalID: journalID + "-ok", InstanceID: instanceID,
		FromVersion: from, ToVersion: bundle.Manifest.Version,
		Status: "succeeded", StartedAtUnixMS: started, FinishedAtUnixMS: i.unixMS(),
	})
}

func packageRecord(bundle plugin.Bundle, now int64) plugin.PackageRecord {
	return plugin.PackageRecord{
		ID:               bundle.Manifest.ID,
		Version:          bundle.Manifest.Version,
		ABIVersion:       plugin.ABIVersion,
		ArtifactSHA256:   bundle.SHA256,
		Manifest:         bundle.Manifest,
		VerifiedAtUnixMS: now,
	}
}

func (i *Installer) unixMS() int64 {
	now := time.Now
	if i != nil && i.now != nil {
		now = i.now
	}
	return max(now().UnixMilli(), int64(1))
}

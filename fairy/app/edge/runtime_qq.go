package edge

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"fairy/plugin"
	"fairy/plugin/qqonebot"
	"fairy/plugin/testhost"
	"fairy/runtime/wasm"
)

func (r *Runtime) bindQQPlugin(ctx context.Context) error {
	if r == nil || r.plugins == nil || r.host == nil || r.facade == nil {
		return nil
	}
	instances, err := r.plugins.Instances(ctx)
	if err != nil {
		return err
	}
	instanceID, ok := qqonebot.Discover(instances)
	if !ok {
		return nil
	}
	var record plugin.InstanceRecord
	for _, instance := range instances {
		if instance.ID == instanceID {
			record = instance
			break
		}
	}
	config, err := qqonebot.ParseInstanceConfig(record.ConfigDocument)
	if err != nil {
		return err
	}
	var httpCall testhost.HostCall
	if config.APIBaseURL != "" {
		grant, err := wasm.HTTPRequestGrantFromURLMethods(config.APIBaseURL, 64<<10, http.MethodPost)
		if err != nil {
			return err
		}
		if err := r.applyQQCredential(ctx, instanceID, grant); err != nil {
			return err
		}
		host := r.host
		httpCall = func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
			return host.HTTPRequest(ctx, wasm.Grant{HTTPRequest: grant}, payload)
		}
	}
	bridge, err := newQQBridge(r.facade, r.ReadStickerContent, instanceID, config, wasm.Grant{}, httpCall)
	if err != nil {
		return err
	}
	r.qq = bridge
	qqCtx, cancel := context.WithCancel(context.Background())
	r.qqCancel = cancel
	if bind, err := loopbackBind(config.IngressBind); err != nil {
		cancel()
		return err
	} else if bind != "" {
		server := &http.Server{Addr: bind, Handler: bridge, ReadHeaderTimeout: 2 * time.Second}
		r.qqHTTP = server
		go func() { _ = server.ListenAndServe() }()
	}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-qqCtx.Done():
				return
			case <-ticker.C:
				_ = bridge.Poll(qqCtx)
			}
		}
	}()
	return nil
}

func (r *Runtime) applyQQCredential(ctx context.Context, instanceID string, grant *wasm.HTTPRequestGrant) error {
	if r.plugins == nil || r.core == nil || r.core.Secret == nil || grant == nil {
		return nil
	}
	refs, err := r.plugins.ConfigRefs(ctx, instanceID)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.Handle != qqCredentialHandle {
			continue
		}
		secret, found, err := r.core.Secret.LoadContext(ctx, ref.SecretName)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		return grant.SetCredential(ref.Handle, secret.Expose())
	}
	return nil
}

package edge

import (
	"context"
	"encoding/json"

	"fairy/context/knowledge"
	"fairy/plugin/testhost"
	"fairy/plugin/websearch"
	"fairy/runtime/config"
	"fairy/runtime/wasm"
)

func (r *Runtime) bindWebPlugin(ctx context.Context) error {
	if r == nil || r.plugins == nil || r.host == nil || r.core == nil {
		return nil
	}
	instances, err := r.plugins.Instances(ctx)
	if err != nil {
		return err
	}
	instanceID, ok := websearch.Discover(instances)
	if !ok {
		return nil
	}
	settings, err := config.ReadWebSearchSettings(r.core.ConfigRoot)
	if err != nil {
		return err
	}
	baseURL := config.ResolveWebSearchBaseURL(settings.BaseURL)
	searchGrant, err := wasm.HTTPRequestGrantFromURL(baseURL, 64<<10)
	if err != nil {
		return err
	}
	searchInvoke, err := newWebPluginInvoker(r.host, searchGrant)
	if err != nil {
		return err
	}
	host := r.host
	r.core.BindWebPlugin(&knowledge.PluginTools{
		SearchInvoke: searchInvoke,
		NewFetch: func(target string) (knowledge.EnvelopeInvoker, error) {
			grant, err := wasm.HTTPRequestGrantFromURL(target, knowledge.DocumentFetchMaxBodyBytes)
			if err != nil {
				return nil, err
			}
			return newWebPluginInvoker(host, grant)
		},
		Ready:      true,
		BaseURL:    baseURL,
		InstanceID: instanceID,
	})
	return nil
}

func newWebPluginInvoker(host *wasm.Host, grant *wasm.HTTPRequestGrant) (knowledge.EnvelopeInvoker, error) {
	var runner *testhost.Host
	runner, err := testhost.New(websearch.NewHandler(func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
		return runner.Call(ctx, capability, payload)
	}), testhost.Options{
		MaxInputBytes: testhost.DefaultOptions().MaxInputBytes,
		MaxCalls:      testhost.DefaultOptions().MaxCalls,
		Capabilities:  []string{"http.request"},
		HostCall: func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
			return host.HTTPRequest(ctx, wasm.Grant{HTTPRequest: grant}, payload)
		},
	})
	if err != nil {
		return nil, err
	}
	return runner.Invoke, nil
}

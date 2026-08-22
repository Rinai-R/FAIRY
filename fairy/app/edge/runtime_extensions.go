//go:build !endpointstrict

package edge

import (
	"context"

	"fairy/plugin"
)

func Open(ctx context.Context, options Options) (*Runtime, error) {
	runtime, err := openRuntimeBase(ctx, options)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = runtime.Close(context.Background())
		}
	}()
	database, err := runtime.core.Foundation.SQL()
	if err != nil {
		return nil, err
	}
	pluginStore, err := plugin.NewStore(database, runtime.core.Foundation.QueryLimit())
	if err != nil {
		return nil, err
	}
	runtime.plugins = pluginStore
	bindings := pluginBindingsForProfile(runtime.core.RuntimeProfile)
	if bindings.web {
		if err := runtime.bindWebPlugin(ctx); err != nil {
			return nil, err
		}
	}
	if bindings.openSERPAdapter {
		runtime.bindOpenSERP()
	}
	if bindings.qq {
		if err := runtime.bindQQPlugin(ctx); err != nil {
			return nil, err
		}
	}
	keep = true
	return runtime, nil
}

type pluginBindings struct {
	openSERPAdapter bool
	web             bool
	qq              bool
}

func pluginBindingsForProfile(profile Profile) pluginBindings {
	if profile == ProfileEndpointStrict {
		return pluginBindings{openSERPAdapter: true}
	}
	return pluginBindings{web: true, qq: true}
}

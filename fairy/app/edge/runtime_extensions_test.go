//go:build !endpointstrict

package edge

import "testing"

func TestEndpointStrictDoesNotBindLegacyNetworkPlugins(t *testing.T) {
	bindings := pluginBindingsForProfile(ProfileEndpointStrict)
	if !bindings.openSERPAdapter || bindings.web || bindings.qq {
		t.Fatalf("endpoint-strict bindings = %#v, want no legacy web or QQ plugin", bindings)
	}
	bindings = pluginBindingsForProfile(ProfileFull)
	if bindings.openSERPAdapter || !bindings.web || !bindings.qq {
		t.Fatalf("full bindings = %#v, want legacy development plugins", bindings)
	}
	bindings = pluginBindingsForProfile(ProfileDesktopLite)
	if bindings.openSERPAdapter || !bindings.web || !bindings.qq {
		t.Fatalf("desktop-lite bindings = %#v, want explicitly non-strict extensions", bindings)
	}
}

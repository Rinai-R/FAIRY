package model

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestEndpointProviderClientRejectsEnvironmentProxyAndRedirects(t *testing.T) {
	client, err := newEndpointProviderClient(
		"https://provider.example.test/v1",
		time.Minute,
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
		},
		func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("fixture dial stopped")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("endpoint provider client inherited an environment proxy")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://provider.example.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); !errors.Is(err, ErrProviderRedirectDenied) {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestProviderPinnedDialerRejectsDifferentTargetAndAddressChange(t *testing.T) {
	lookupCalls := 0
	dialer := &providerPinnedDialer{
		host: "provider.example.test",
		port: "443",
		lookup: func(context.Context, string) ([]net.IPAddr, error) {
			lookupCalls++
			if lookupCalls == 1 {
				return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
			}
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.11")}}, nil
		},
		dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("fixture dial stopped")
		},
	}
	if _, err := dialer.DialContext(t.Context(), "tcp", "provider.example.test:443"); err == nil || errors.Is(err, ErrProviderAddressDenied) {
		t.Fatalf("first dial error = %v, want fixture error after pin", err)
	}
	if _, err := dialer.DialContext(t.Context(), "tcp", "provider.example.test:443"); !errors.Is(err, ErrProviderAddressDenied) {
		t.Fatalf("address-change error = %v", err)
	}
	if _, err := dialer.DialContext(t.Context(), "tcp", "other.example.test:443"); !errors.Is(err, ErrProviderAddressDenied) {
		t.Fatalf("different-target error = %v", err)
	}
}

func TestProviderEndpointAddressRejectsUnsafeURLs(t *testing.T) {
	for _, raw := range []string{
		"", "ftp://provider.example.test", "https://user:secret@provider.example.test",
		"https://provider.example.test?token=secret", "https://provider.example.test#fragment",
		"https://例子.example", "http://localhost:11434/v1", "http://127.0.0.1:8080/v1", "http://[::1]:8080/v1",
	} {
		if _, _, err := providerEndpointAddress(raw); !errors.Is(err, ErrProviderOriginInvalid) {
			t.Fatalf("providerEndpointAddress(%q) error = %v", raw, err)
		}
	}
}

func TestProviderPinnedDialerRejectsSavedHostResolvingToLoopback(t *testing.T) {
	dialCalled := false
	client, err := newEndpointProviderClient(
		"https://provider.example.test/v1",
		time.Second,
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("::1")}}, nil
		},
		func(context.Context, string, string) (net.Conn, error) {
			dialCalled = true
			return nil, errors.New("unexpected dial")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://provider.example.test/v1/probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(request); !errors.Is(err, ErrProviderAddressDenied) {
		t.Fatalf("client.Do() error = %v, want %v", err, ErrProviderAddressDenied)
	}
	if dialCalled {
		t.Fatal("loopback provider resolution reached the network dialer")
	}
}

func TestEndpointProviderClientsDialOnlyTheirDeclaredOrigins(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   string
		resolvedIP string
		wantDial   string
	}{
		{name: "chat", endpoint: "http://chat.example.test:8443/v1", resolvedIP: "192.0.2.20", wantDial: "192.0.2.20:8443"},
		{name: "embedding", endpoint: "http://embedding.example.test:9443/v1", resolvedIP: "192.0.2.30", wantDial: "192.0.2.30:9443"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dials := make(chan string, 2)
			requests := make(chan string, 2)
			client, err := newEndpointProviderClient(
				test.endpoint,
				time.Second,
				func(context.Context, string) ([]net.IPAddr, error) {
					return []net.IPAddr{{IP: net.ParseIP(test.resolvedIP)}}, nil
				},
				func(_ context.Context, _, address string) (net.Conn, error) {
					dials <- address
					clientSide, serverSide := net.Pipe()
					go serveOneProviderRequest(serverSide, requests)
					return clientSide, nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, strings.TrimRight(test.endpoint, "/")+"/probe", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if got := <-dials; got != test.wantDial {
				t.Fatalf("dial target = %q, want %q", got, test.wantDial)
			}
			if got := <-requests; got != "/v1/probe" {
				t.Fatalf("request path = %q, want /v1/probe", got)
			}
			select {
			case extra := <-dials:
				t.Fatalf("unexpected extra dial target %q", extra)
			default:
			}
		})
	}
}

func serveOneProviderRequest(connection net.Conn, requests chan<- string) {
	defer connection.Close()
	request, err := http.ReadRequest(bufio.NewReader(connection))
	if err != nil {
		return
	}
	requests <- request.URL.Path
	_, _ = connection.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"))
}

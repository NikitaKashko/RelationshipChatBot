package collection

import (
	"context"
	"net"
	"net/http"
	"testing"
)

func TestResolverDeniesPrivateAndUntrustedLinks(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "0.0.0.0", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "192.0.2.1", "2001:db8::1"} {
		if PublicIP(net.ParseIP(address)) {
			t.Fatal("accepted private/reserved target", address)
		}
	}
	for _, raw := range []string{"https://127.0.0.1/maps", "http://yandex.ru/maps", "https://yandex.ru.evil.example/maps", "https://user:pass@yandex.ru/maps", "https://yandex.ru:443/maps"} {
		if _, err := ResolveMap(context.Background(), raw); err == nil {
			t.Fatal("accepted unsafe link", raw)
		}
	}
	if !PublicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("rejected public IP")
	}
	if !mapLink("https://yandex.ru/maps/org/example/123") {
		t.Fatal("rejected map reference")
	}
}

func TestEveryRedirectStaysOnPermittedHTTPSHost(t *testing.T) {
	for _, raw := range []string{"https://localhost/maps", "https://10.0.0.1/", "http://yandex.ru/maps", "https://evil.example/maps"} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		if err := mapRedirect(req, nil); err == nil {
			t.Fatal("unsafe redirect", raw)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://yandex.ru/maps/org/123", nil)
	if err := mapRedirect(req, make([]*http.Request, 4)); err == nil {
		t.Fatal("redirect limit not enforced")
	}
	if err := mapRedirect(req, make([]*http.Request, 3)); err != nil {
		t.Fatal(err)
	}
}

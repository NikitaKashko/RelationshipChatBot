package collection

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
)

var blocked = []netip.Prefix{netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32")}

func PublicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return true
}
func mapLink(raw string) bool {
	if !ValidURL(raw) {
		return false
	}
	u, _ := url.Parse(raw)
	switch u.Hostname() {
	case "yandex.ru", "yandex.com", "yandex.by", "yandex.kz", "maps.yandex.ru":
		return true
	}
	return false
}
func mapRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 3 || !mapLink(req.URL.String()) {
		return errors.New("redirect denied")
	}
	return nil
}

// ResolveMap follows at most three redirects, validates every DNS result, and
// dials the already validated IP to prevent a second DNS lookup/rebinding.
// It resolves a reference only; it does not scrape catalog fields or photos.
func ResolveMap(ctx context.Context, raw string) (string, error) {
	if !mapLink(raw) {
		return "", errors.New(texts.T("internal.collection.resolver.001"))
	}
	tr := &http.Transport{TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("no public address")
		}
		for _, ip := range ips {
			if !PublicIP(ip.IP) {
				return nil, errors.New("non-public target denied")
			}
		}
		d := net.Dialer{Timeout: 5 * time.Second}
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 12 * time.Second, CheckRedirect: mapRedirect}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New(texts.T("internal.collection.resolver.002"))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.New(texts.T("internal.collection.resolver.002"))
	}
	const limit = 1024 * 1024
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, limit+1))
	if err != nil || n > limit {
		return "", errors.New(texts.T("internal.collection.resolver.003"))
	}
	return resp.Request.URL.String(), nil
}

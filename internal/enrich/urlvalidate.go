package enrich

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// blockedMetadataHosts are hostnames that must never receive CDC payloads.
var blockedMetadataHosts = map[string]struct{}{
	"metadata.google.internal": {},
	"metadata.goog":            {},
}

// urlPolicy governs SSRF destination controls for the enricher endpoint.
type urlPolicy struct {
	allowHosts           map[string]struct{}
	insecureAllowPrivate bool
}

func newURLPolicy(allowHosts []string, insecureAllowPrivate bool) *urlPolicy {
	p := &urlPolicy{
		insecureAllowPrivate: insecureAllowPrivate,
	}
	if len(allowHosts) > 0 {
		p.allowHosts = make(map[string]struct{}, len(allowHosts))
		for _, h := range allowHosts {
			h = strings.ToLower(strings.TrimSpace(h))
			if h != "" {
				p.allowHosts[h] = struct{}{}
			}
		}
	}
	return p
}

func validateEnricherURL(rawURL string, policy *urlPolicy) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("url: invalid enricher endpoint %q", rawURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("url: unsupported scheme %q (want http or https)", u.Scheme)
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, fmt.Errorf("url: missing host in %q", rawURL)
	}
	if _, blocked := blockedMetadataHosts[host]; blocked {
		return nil, fmt.Errorf("url: blocked metadata host %q", host)
	}
	if policy.isAllowlisted(host) {
		return u, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if policy.isBlockedIP(ip) {
			return nil, fmt.Errorf("url: blocked destination %s", host)
		}
		return u, nil
	}
	return u, nil
}

func (p *urlPolicy) isAllowlisted(host string) bool {
	if p == nil || len(p.allowHosts) == 0 {
		return false
	}
	_, ok := p.allowHosts[strings.ToLower(host)]
	return ok
}

func (p *urlPolicy) isBlockedIP(ip net.IP) bool {
	if p == nil || ip == nil {
		return false
	}
	if p.insecureAllowPrivate {
		return false
	}
	if p.isAllowlisted(ip.String()) {
		return false
	}
	// Loopback is permitted for local sidecars and httptest.
	if ip.IsLoopback() {
		return false
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	// AWS/GCP/Azure link-local metadata range.
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 169 && ip4[1] == 254 {
		return true
	}
	return false
}

func (p *urlPolicy) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	host = strings.Trim(host, "[]")

	if ip := net.ParseIP(host); ip != nil {
		if p.isBlockedIP(ip) {
			return nil, fmt.Errorf("enrichment: blocked destination %s", host)
		}
	} else if !p.isAllowlisted(host) {
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ia := range ips {
			if p.isBlockedIP(ia.IP) {
				return nil, fmt.Errorf("enrichment: blocked destination %s (%s)", host, ia.IP)
			}
		}
	}

	d := &net.Dialer{}
	return d.DialContext(ctx, network, addr)
}

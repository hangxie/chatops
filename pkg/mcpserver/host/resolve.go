package host

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"time"
)

const checkResolve = "resolve"

// lookup is the part of *net.Resolver the resolve check uses.
type lookup interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
	LookupCNAME(ctx context.Context, host string) (string, error)
}

// resolved is a host's resolve check and, when it passed, the address every probe targets.
type resolved struct {
	check     Check
	canonical string
	addresses []netip.Addr
	selected  netip.Addr
}

// resolve looks the address up and selects the first IPv4 address, else the first IPv6 one, for every probe.
func resolve(ctx context.Context, r lookup, timeout time.Duration, address string) resolved {
	if ip, err := netip.ParseAddr(address); err == nil {
		return resolved{check: Check{Name: checkResolve, Outcome: OutcomePass, Detail: ip.String()}, addresses: []netip.Addr{ip}, selected: ip}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	records, err := r.LookupIPAddr(ctx, address)
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound, err == nil && len(records) == 0:
		return resolved{check: Check{Name: checkResolve, Outcome: OutcomeFail, Detail: "name does not exist in DNS"}}
	case err != nil:
		return resolved{check: Check{Name: checkResolve, Outcome: OutcomeError, Detail: "DNS lookup failed: " + err.Error()}}
	}
	result := resolved{}
	for _, record := range records {
		if ip, ok := netip.AddrFromSlice(record.IP); ok {
			ip = ip.Unmap().WithZone(record.Zone)
			result.addresses = append(result.addresses, ip)
			if !result.selected.IsValid() || (ip.Is4() && !result.selected.Is4()) {
				result.selected = ip
			}
		}
	}
	if !result.selected.IsValid() {
		return resolved{check: Check{Name: checkResolve, Outcome: OutcomeError, Detail: "DNS lookup returned no usable address"}}
	}
	if canonical, err := r.LookupCNAME(ctx, address); err == nil && !sameName(canonical, address) {
		result.canonical = strings.TrimSuffix(canonical, ".")
	}
	result.check = Check{Name: checkResolve, Outcome: OutcomePass, Detail: result.describe()}
	return result
}

func (r resolved) describe() string {
	detail := r.selected.String()
	if len(r.addresses) > 1 {
		all := make([]string, len(r.addresses))
		for i, ip := range r.addresses {
			all[i] = ip.String()
		}
		detail += " (of " + strings.Join(all, ", ") + ")"
	}
	if r.canonical != "" {
		detail += " via " + r.canonical
	}
	return detail
}

func sameName(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}

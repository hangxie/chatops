package host

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeLookup answers from fixed records, or blocks until the context ends when block is set.
type fakeLookup struct {
	records   []net.IPAddr
	err       error
	canonical string
	cnameErr  error
	block     bool
}

func (f fakeLookup) LookupIPAddr(ctx context.Context, _ string) ([]net.IPAddr, error) {
	if f.block {
		<-ctx.Done()
		return nil, &net.DNSError{Err: ctx.Err().Error(), IsTimeout: true}
	}
	return f.records, f.err
}

func (f fakeLookup) LookupCNAME(context.Context, string) (string, error) {
	return f.canonical, f.cnameErr
}

func ips(addresses ...string) []net.IPAddr {
	var records []net.IPAddr
	for _, a := range addresses {
		ip := netip.MustParseAddr(a)
		records = append(records, net.IPAddr{IP: ip.AsSlice(), Zone: ip.Zone()})
	}
	return records
}

func Test_resolve(t *testing.T) {
	tests := map[string]struct {
		address  string
		lookup   fakeLookup
		selected string
		check    Check
	}{
		"single-ipv4":      {address: "media.lan", lookup: fakeLookup{records: ips("192.168.0.201"), canonical: "media.lan."}, selected: "192.168.0.201", check: Check{Name: "resolve", Outcome: OutcomePass, Detail: "192.168.0.201"}},
		"ipv4-preferred":   {address: "media.lan", lookup: fakeLookup{records: ips("2001:db8::10", "192.168.1.10", "192.168.1.11")}, selected: "192.168.1.10", check: Check{Name: "resolve", Outcome: OutcomePass, Detail: "192.168.1.10 (of 2001:db8::10, 192.168.1.10, 192.168.1.11)"}},
		"ipv6-only":        {address: "v6.lan", lookup: fakeLookup{records: ips("2001:db8::10", "2001:db8::11")}, selected: "2001:db8::10", check: Check{Name: "resolve", Outcome: OutcomePass, Detail: "2001:db8::10 (of 2001:db8::10, 2001:db8::11)"}},
		"mapped-ipv4":      {address: "m.lan", lookup: fakeLookup{records: []net.IPAddr{{IP: net.ParseIP("::ffff:10.0.0.1")}}}, selected: "10.0.0.1", check: Check{Name: "resolve", Outcome: OutcomePass, Detail: "10.0.0.1"}},
		"alias":            {address: "ai.xiehang.com", lookup: fakeLookup{records: ips("192.168.0.202"), canonical: "mbp16.xiehang.com."}, selected: "192.168.0.202", check: Check{Name: "resolve", Outcome: OutcomePass, Detail: "192.168.0.202 via mbp16.xiehang.com"}},
		"cname-error":      {address: "ai.lan", lookup: fakeLookup{records: ips("192.168.0.202"), cnameErr: errors.New("boom")}, selected: "192.168.0.202", check: Check{Name: "resolve", Outcome: OutcomePass, Detail: "192.168.0.202"}},
		"ip-literal":       {address: "192.168.0.200", lookup: fakeLookup{err: errors.New("not called")}, selected: "192.168.0.200", check: Check{Name: "resolve", Outcome: OutcomePass, Detail: "192.168.0.200"}},
		"no-usable-ip":     {address: "odd.lan", lookup: fakeLookup{records: []net.IPAddr{{}, {IP: net.IP{1, 2, 3}}}}, check: Check{Name: "resolve", Outcome: OutcomeError, Detail: "DNS lookup returned no usable address"}},
		"nxdomain":         {address: "ghost.lan", lookup: fakeLookup{err: &net.DNSError{Err: "no such host", Name: "ghost.lan", IsNotFound: true}}, check: Check{Name: "resolve", Outcome: OutcomeFail, Detail: "name does not exist in DNS"}},
		"no-records":       {address: "empty.lan", lookup: fakeLookup{}, check: Check{Name: "resolve", Outcome: OutcomeFail, Detail: "name does not exist in DNS"}},
		"servfail":         {address: "media.lan", lookup: fakeLookup{err: &net.DNSError{Err: "server misbehaving", Name: "media.lan", IsTemporary: true}}, check: Check{Name: "resolve", Outcome: OutcomeError, Detail: "DNS lookup failed: lookup media.lan: server misbehaving"}},
		"resolver-timeout": {address: "media.lan", lookup: fakeLookup{block: true}, check: Check{Name: "resolve", Outcome: OutcomeError, Detail: "DNS lookup failed: lookup : context deadline exceeded"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := resolve(context.Background(), tc.lookup, 20*time.Millisecond, tc.address)
			require.Equal(t, tc.check, got.check)
			if tc.selected == "" {
				require.False(t, got.selected.IsValid())
				return
			}
			require.Equal(t, tc.selected, got.selected.String())
		})
	}
}

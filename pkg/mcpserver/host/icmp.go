package host

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const echoCount = 3

var echoPayload = []byte("chatops-mcp")

// listenFunc is icmp.ListenPacket, replaced in tests.
type listenFunc func(network, address string) (net.PacketConn, error)

func listenICMP(network, address string) (net.PacketConn, error) {
	return icmp.ListenPacket(network, address)
}

// family holds what differs between ICMPv4 and ICMPv6 echoes.
type family struct {
	network, listen string
	proto           int
	request, reply  icmp.Type
}

var (
	family4 = family{network: "udp4", listen: "0.0.0.0", proto: 1, request: ipv4.ICMPTypeEcho, reply: ipv4.ICMPTypeEchoReply}
	family6 = family{network: "udp6", listen: "::", proto: 58, request: ipv6.ICMPTypeEchoRequest, reply: ipv6.ICMPTypeEchoReply}
)

// checkPing sends three echoes over an unprivileged ICMP socket and counts the replies that match them.
func checkPing(ctx context.Context, listen listenFunc, timeout time.Duration, ip netip.Addr) Check {
	fam := family4
	if !ip.Is4() {
		fam = family6
	}
	conn, err := listen(fam.network, fam.listen)
	switch {
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return Check{Name: CheckPing, Outcome: OutcomeError, Detail: "ICMP not permitted: " + err.Error()}
	case err != nil:
		return Check{Name: CheckPing, Outcome: OutcomeError, Detail: "open ICMP socket: " + err.Error()}
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	probe := echoProbe{fam: fam, target: ip, id: echoID(conn), sent: map[int]time.Time{}}
	first := rand.IntN(1<<16 - echoCount)
	for seq := first; seq < first+echoCount; seq++ {
		if err := probe.send(conn, seq); err != nil {
			return netFailure(CheckPing, err, timeout)
		}
	}
	rtts, err := probe.collect(conn)
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return netFailure(CheckPing, ctx.Err(), timeout)
	case len(rtts) == 0 && err != nil && !errors.Is(err, os.ErrDeadlineExceeded):
		return netFailure(CheckPing, err, timeout)
	case len(rtts) == 0:
		return Check{Name: CheckPing, Outcome: OutcomeFail, Detail: fmt.Sprintf("no reply to %d echoes within %s", echoCount, timeout)}
	}
	var total time.Duration
	for _, rtt := range rtts {
		total += rtt
	}
	average := (total / time.Duration(len(rtts))).Round(10 * time.Microsecond)
	return Check{Name: CheckPing, Outcome: OutcomePass, Detail: fmt.Sprintf("%d/%d replies, average %s", len(rtts), echoCount, average)}
}

// echoID is the socket's port where the kernel rewrites echo identifiers to it (Linux), else a random identifier.
func echoID(conn net.PacketConn) int {
	if local, ok := conn.LocalAddr().(*net.UDPAddr); ok && local.Port != 0 {
		return local.Port
	}
	return rand.IntN(1 << 16)
}

// echoProbe tracks the echoes one check sent, so only their own replies count.
type echoProbe struct {
	fam    family
	target netip.Addr
	id     int
	sent   map[int]time.Time
}

func (p echoProbe) send(conn net.PacketConn, seq int) error {
	// Marshal fails only for body types it does not know, never for an echo.
	message, _ := (&icmp.Message{Type: p.fam.request, Body: &icmp.Echo{ID: p.id, Seq: seq, Data: echoPayload}}).Marshal(nil)
	if _, err := conn.WriteTo(message, &net.UDPAddr{IP: p.target.AsSlice(), Zone: p.target.Zone()}); err != nil {
		return err
	}
	p.sent[seq] = time.Now()
	return nil
}

// collect reads until every echo is answered or the deadline passes, returning each matched round-trip time.
func (p echoProbe) collect(conn net.PacketConn) ([]time.Duration, error) {
	var rtts []time.Duration
	buffer := make([]byte, 1500)
	for len(p.sent) != 0 {
		n, peer, err := conn.ReadFrom(buffer)
		if err != nil {
			return rtts, err
		}
		if seq, ok := p.match(buffer[:n], peer); ok {
			rtts = append(rtts, time.Since(p.sent[seq]))
			delete(p.sent, seq)
		}
	}
	return rtts, nil
}

// match accepts only an echo reply from the target carrying this probe's identifier and an outstanding sequence number.
func (p echoProbe) match(packet []byte, peer net.Addr) (int, bool) {
	message, err := icmp.ParseMessage(p.fam.proto, packet)
	if err != nil || message.Type != p.fam.reply {
		return 0, false
	}
	echo, ok := message.Body.(*icmp.Echo)
	if !ok || echo.ID != p.id {
		return 0, false
	}
	if _, outstanding := p.sent[echo.Seq]; !outstanding || !sameAddr(peer, p.target) {
		return 0, false
	}
	return echo.Seq, true
}

func sameAddr(peer net.Addr, target netip.Addr) bool {
	var ip net.IP
	switch a := peer.(type) {
	case *net.UDPAddr:
		ip = a.IP
	case *net.IPAddr:
		ip = a.IP
	}
	got, ok := netip.AddrFromSlice(ip)
	return ok && got.Unmap() == target.WithZone("")
}

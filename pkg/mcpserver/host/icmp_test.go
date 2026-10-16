package host

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type fakePacket struct {
	data []byte
	from net.Addr
}

// fakeICMP is an ICMP socket whose responder decides what each sent echo brings back.
type fakeICMP struct {
	mu       sync.Mutex
	deadline time.Time
	queue    chan fakePacket
	wake     chan struct{}
	port     int
	writeErr error
	readErr  error
	sent     []*icmp.Echo
	respond  func(echo *icmp.Echo, to netip.Addr) []fakePacket
}

func newFakeICMP(respond func(*icmp.Echo, netip.Addr) []fakePacket) *fakeICMP {
	return &fakeICMP{queue: make(chan fakePacket, 64), wake: make(chan struct{}, 1), respond: respond}
}

func (c *fakeICMP) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		deadline, readErr := c.deadline, c.readErr
		c.mu.Unlock()
		if readErr != nil {
			return 0, nil, readErr
		}
		var expired <-chan time.Time
		if !deadline.IsZero() {
			wait := time.Until(deadline)
			if wait <= 0 {
				return 0, nil, os.ErrDeadlineExceeded
			}
			expired = time.After(wait)
		}
		select {
		case p := <-c.queue:
			return copy(b, p.data), p.from, nil
		case <-expired:
			return 0, nil, os.ErrDeadlineExceeded
		case <-c.wake:
		}
	}
}

func (c *fakeICMP) WriteTo(b []byte, addr net.Addr) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	to := addr.(*net.UDPAddr).AddrPort().Addr().Unmap()
	proto := 1
	if !to.Is4() {
		proto = 58
	}
	message, err := icmp.ParseMessage(proto, b)
	if err != nil {
		return 0, err
	}
	echo := message.Body.(*icmp.Echo)
	c.mu.Lock()
	c.sent = append(c.sent, echo)
	c.mu.Unlock()
	for _, p := range c.respond(echo, to) {
		c.queue <- p
	}
	return len(b), nil
}

func (c *fakeICMP) Close() error { return nil }

func (c *fakeICMP) LocalAddr() net.Addr { return &net.UDPAddr{Port: c.port} }

func (c *fakeICMP) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}

func (c *fakeICMP) SetReadDeadline(t time.Time) error { return c.SetDeadline(t) }

func (c *fakeICMP) SetWriteDeadline(time.Time) error { return nil }

func packet(typ icmp.Type, id, seq int, from netip.Addr) fakePacket {
	data, _ := (&icmp.Message{Type: typ, Body: &icmp.Echo{ID: id, Seq: seq, Data: echoPayload}}).Marshal(nil)
	return fakePacket{data: data, from: &net.UDPAddr{IP: from.AsSlice()}}
}

func replyType(to netip.Addr) icmp.Type {
	if to.Is4() {
		return ipv4.ICMPTypeEchoReply
	}
	return ipv6.ICMPTypeEchoReply
}

// answer replies to the echoes keep selects, sent to one of only, or to any address when only is empty.
func answer(keep func(seq int) bool, only ...netip.Addr) func(*icmp.Echo, netip.Addr) []fakePacket {
	return func(echo *icmp.Echo, to netip.Addr) []fakePacket {
		if !keep(echo.Seq) || (len(only) != 0 && !slices.Contains(only, to)) {
			return nil
		}
		return []fakePacket{packet(replyType(to), echo.ID, echo.Seq, to)}
	}
}

func listenTo(conn net.PacketConn) listenFunc {
	return func(string, string) (net.PacketConn, error) { return conn, nil }
}

var (
	target4 = netip.MustParseAddr("192.168.0.201")
	target6 = netip.MustParseAddr("2001:db8::10")
	other   = netip.MustParseAddr("192.168.0.99")
)

func Test_checkPing(t *testing.T) {
	everything := func(int) bool { return true }
	tests := map[string]struct {
		target  netip.Addr
		respond func(*icmp.Echo, netip.Addr) []fakePacket
		outcome Outcome
		detail  string
	}{
		"all-answered":  {target: target4, respond: answer(everything), outcome: OutcomePass, detail: "3/3 replies, average "},
		"ipv6-answered": {target: target6, respond: answer(everything), outcome: OutcomePass, detail: "3/3 replies, average "},
		"none-answered": {target: target4, respond: answer(func(int) bool { return false }), outcome: OutcomeFail, detail: "no reply to 3 echoes within 50ms"},
		"one-answered": {target: target4, respond: func(echo *icmp.Echo, to netip.Addr) []fakePacket {
			if echo.Seq%3 != 0 {
				return nil
			}
			return []fakePacket{packet(replyType(to), echo.ID, echo.Seq, to)}
		}, outcome: OutcomePass, detail: "1/3 replies, average "},
		"own-request-echoed-back": {target: target6, respond: func(echo *icmp.Echo, to netip.Addr) []fakePacket {
			return []fakePacket{packet(ipv6.ICMPTypeEchoRequest, echo.ID, echo.Seq, to)}
		}, outcome: OutcomeFail, detail: "no reply to 3 echoes"},
		"foreign-replies-ignored": {target: target4, respond: func(echo *icmp.Echo, to netip.Addr) []fakePacket {
			return []fakePacket{
				packet(ipv4.ICMPTypeEchoReply, echo.ID+1, echo.Seq, to),
				packet(ipv4.ICMPTypeEchoReply, echo.ID, echo.Seq+1000, to),
				packet(ipv4.ICMPTypeEchoReply, echo.ID, echo.Seq, other),
				packet(ipv4.ICMPTypeDestinationUnreachable, echo.ID, echo.Seq, to),
				{data: []byte{1, 2}, from: &net.UDPAddr{IP: to.AsSlice()}},
			}
		}, outcome: OutcomeFail, detail: "no reply to 3 echoes"},
		"duplicate-reply-counted-once": {target: target4, respond: func(echo *icmp.Echo, to netip.Addr) []fakePacket {
			reply := packet(ipv4.ICMPTypeEchoReply, echo.ID, echo.Seq, to)
			if echo.Seq%3 == 0 {
				return []fakePacket{reply, reply}
			}
			return nil
		}, outcome: OutcomePass, detail: "1/3 replies"},
		"ip-addr-peer": {target: target4, respond: func(echo *icmp.Echo, to netip.Addr) []fakePacket {
			p := packet(ipv4.ICMPTypeEchoReply, echo.ID, echo.Seq, to)
			p.from = &net.IPAddr{IP: to.AsSlice()}
			return []fakePacket{p}
		}, outcome: OutcomePass, detail: "3/3 replies"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			conn := newFakeICMP(tc.respond)
			got := checkPing(context.Background(), listenTo(conn), 50*time.Millisecond, tc.target)
			require.Equal(t, "ping", got.Name)
			require.Equal(t, tc.outcome, got.Outcome, got.Detail)
			require.True(t, strings.HasPrefix(got.Detail, tc.detail), got.Detail)
			require.Len(t, conn.sent, echoCount)
			for i, echo := range conn.sent {
				require.Equal(t, conn.sent[0].ID, echo.ID)
				require.Equal(t, conn.sent[0].Seq+i, echo.Seq)
			}
		})
	}
}

func Test_checkPing_kernel_identifier(t *testing.T) {
	conn := newFakeICMP(answer(func(int) bool { return true }))
	conn.port = 4242
	got := checkPing(context.Background(), listenTo(conn), time.Second, target4)
	require.Equal(t, OutcomePass, got.Outcome)
	require.Equal(t, 4242, conn.sent[0].ID)
}

func Test_checkPing_errors(t *testing.T) {
	tests := map[string]struct {
		listen  listenFunc
		conn    *fakeICMP
		outcome Outcome
		detail  string
	}{
		"not-permitted": {listen: func(string, string) (net.PacketConn, error) {
			return nil, os.NewSyscallError("socket", syscall.EACCES)
		}, outcome: OutcomeError, detail: "ICMP not permitted: socket: permission denied"},
		"eperm": {listen: func(string, string) (net.PacketConn, error) {
			return nil, os.NewSyscallError("socket", syscall.EPERM)
		}, outcome: OutcomeError, detail: "ICMP not permitted: socket: operation not permitted"},
		"socket-error": {listen: func(string, string) (net.PacketConn, error) {
			return nil, errors.New("too many open files")
		}, outcome: OutcomeError, detail: "open ICMP socket: too many open files"},
		"host-unreachable": {conn: &fakeICMP{writeErr: os.NewSyscallError("sendto", syscall.EHOSTUNREACH)}, outcome: OutcomeFail, detail: "no route to host"},
		"net-unreachable":  {conn: &fakeICMP{writeErr: os.NewSyscallError("sendto", syscall.ENETUNREACH)}, outcome: OutcomeError, detail: "network is unreachable from chatops-mcp"},
		"read-error":       {conn: &fakeICMP{readErr: errors.New("socket closed")}, outcome: OutcomeError, detail: "socket closed"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			listen := tc.listen
			if tc.conn != nil {
				tc.conn.wake = make(chan struct{}, 1)
				tc.conn.queue = make(chan fakePacket, 8)
				tc.conn.respond = func(*icmp.Echo, netip.Addr) []fakePacket { return nil }
				listen = listenTo(tc.conn)
			}
			require.Equal(t, Check{Name: "ping", Outcome: tc.outcome, Detail: tc.detail}, checkPing(context.Background(), listen, time.Second, target4))
		})
	}
}

func Test_checkPing_cancelled(t *testing.T) {
	conn := newFakeICMP(answer(func(int) bool { return false }))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	got := checkPing(ctx, listenTo(conn), 5*time.Second, target4)
	require.Equal(t, Check{Name: "ping", Outcome: OutcomeError, Detail: "check cancelled"}, got)
}

// Test_checkPing_loopback uses a real unprivileged ICMP socket, so it also covers the kernel's identifier handling.
func Test_checkPing_loopback(t *testing.T) {
	got := checkPing(context.Background(), listenICMP, 3*time.Second, netip.MustParseAddr("127.0.0.1"))
	if got.Outcome == OutcomeError {
		t.Skipf("unprivileged ICMP is not available here: %s", got.Detail)
	}
	require.Equal(t, OutcomePass, got.Outcome, got.Detail)
	require.True(t, strings.HasPrefix(got.Detail, "3/3 replies"), got.Detail)
}

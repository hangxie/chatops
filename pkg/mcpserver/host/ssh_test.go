package host

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var loopback = netip.MustParseAddr("127.0.0.1")

// serveBanner accepts connections on a loopback port and writes reply, then closes or holds the connection.
func serveBanner(t *testing.T, reply string, hangUp bool) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		_ = listener.Close()
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = conn.Write([]byte(reply))
				if !hangUp {
					<-done
				}
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func Test_checkSSH(t *testing.T) {
	tests := map[string]struct {
		reply  string
		hangUp bool
		want   Check
	}{
		"openssh":         {reply: "SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13\r\n", want: Check{Name: "ssh", Outcome: OutcomePass, Detail: "OpenSSH_9.6p1 Ubuntu-3ubuntu13"}},
		"dropbear":        {reply: "SSH-2.0-dropbear_2022.83\r\n", want: Check{Name: "ssh", Outcome: OutcomePass, Detail: "dropbear_2022.83"}},
		"compat-1.99":     {reply: "SSH-1.99-OpenSSH_3.9\r\n", want: Check{Name: "ssh", Outcome: OutcomePass, Detail: "OpenSSH_3.9"}},
		"lf-only":         {reply: "SSH-2.0-Go\n", want: Check{Name: "ssh", Outcome: OutcomePass, Detail: "Go"}},
		"preamble-lines":  {reply: "Welcome\r\nauthorized use only\r\nSSH-2.0-OpenSSH_9.6\r\n", want: Check{Name: "ssh", Outcome: OutcomePass, Detail: "OpenSSH_9.6"}},
		"ssh-1.5":         {reply: "SSH-1.5-OldServer\r\n", want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: `not an SSH 2 identification: "SSH-1.5-OldServer"`, Responded: true}},
		"ssh-garbage":     {reply: "SSH-garbage\r\n", want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: `not an SSH 2 identification: "SSH-garbage"`, Responded: true}},
		"empty-software":  {reply: "SSH-2.0-\r\n", want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: `not an SSH 2 identification: "SSH-2.0-"`, Responded: true}},
		"http-server":     {reply: "HTTP/1.1 400 Bad Request\r\n\r\n", hangUp: true, want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "connection closed before an SSH identification", Responded: true}},
		"long-line":       {reply: strings.Repeat("x", 300) + "\r\n", want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "line longer than 255 bytes", Responded: true}},
		"max-line":        {reply: "SSH-2.0-" + strings.Repeat("x", 245) + "\r\n", want: Check{Name: "ssh", Outcome: OutcomePass, Detail: strings.Repeat("x", 245)}},
		"no-ident-in-8k":  {reply: strings.Repeat(strings.Repeat("y", 99)+"\n", 100), want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "no SSH identification in the first 8192 bytes", Responded: true}},
		"silent-server":   {want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "connected, but no answer within 100ms", Responded: true}},
		"partial-no-eol":  {reply: "SSH-2.0-Open", want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "connected, but no answer within 100ms", Responded: true}},
		"closed-silently": {hangUp: true, want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "connection closed before an SSH identification", Responded: true}},
	}

	dialer := &net.Dialer{}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			port := serveBanner(t, tc.reply, tc.hangUp)
			require.Equal(t, tc.want, checkSSH(context.Background(), dialer.DialContext, 100*time.Millisecond, loopback, port))
		})
	}
}

func Test_checkSSH_refused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())

	got := checkSSH(context.Background(), (&net.Dialer{}).DialContext, time.Second, loopback, port)
	require.Equal(t, Check{Name: "ssh", Outcome: OutcomeFail, Detail: "connection refused", Responded: true}, got)
}

func Test_checkSSH_dial_errors(t *testing.T) {
	tests := map[string]struct {
		err  error
		want Check
	}{
		"net-unreachable":  {err: dialErr(syscall.ENETUNREACH), want: Check{Name: "ssh", Outcome: OutcomeError, Detail: "network is unreachable from chatops-mcp"}},
		"host-unreachable": {err: dialErr(syscall.EHOSTUNREACH), want: Check{Name: "ssh", Outcome: OutcomeFail, Detail: "no route to host"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var dialed string
			dial := func(_ context.Context, _, address string) (net.Conn, error) {
				dialed = address
				return nil, tc.err
			}
			require.Equal(t, tc.want, checkSSH(context.Background(), dial, time.Second, netip.MustParseAddr("2001:db8::10"), 2222))
			require.Equal(t, "[2001:db8::10]:2222", dialed)
		})
	}
}

func Test_identError(t *testing.T) {
	require.EqualError(t, identError("line longer than 255 bytes"), "line longer than 255 bytes")
}

func Test_checkSSH_cancelled(t *testing.T) {
	port := serveBanner(t, "", false)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	got := checkSSH(ctx, (&net.Dialer{}).DialContext, 5*time.Second, loopback, port)
	require.Equal(t, Check{Name: "ssh", Outcome: OutcomeError, Detail: "check cancelled"}, got)
}

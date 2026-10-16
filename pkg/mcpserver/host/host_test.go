package host

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func connect(t *testing.T, register func(*mcp.Server)) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	register(server)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// mapLookup resolves names from a fixed table; a missing name is NXDOMAIN.
type mapLookup map[string][]net.IPAddr

func (m mapLookup) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if records, ok := m[host]; ok {
		return records, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (m mapLookup) LookupCNAME(_ context.Context, host string) (string, error) {
	return "box-" + host + ".", nil
}

var testConfig = Config{Hosts: []Host{
	{Name: "media", Display: "media", Covers: []string{"the media server"}, Address: "media.lan"},
	{Name: "pi", Display: "pi", Address: "pi.lan"},
	{Name: "nas", Display: "NAS", Address: "nas.lan", Checks: []string{"ping"}},
}}

// testChecker resolves media and nas to loopback, where an SSH banner is served and only media answers pings.
func testChecker(t *testing.T) *checker {
	port := serveBanner(t, "SSH-2.0-OpenSSH_9.6\r\n", false)
	hosts := append([]Host(nil), testConfig.Hosts...)
	hosts[0].SSHPort = port
	pingable := netip.MustParseAddr("127.0.0.1")
	return &checker{
		hosts:   hosts,
		lookup:  mapLookup{"media.lan": ips("127.0.0.1"), "nas.lan": ips("127.0.0.2")},
		dial:    (&net.Dialer{}).DialContext,
		timeout: 100 * time.Millisecond,
		listen: func(string, string) (net.PacketConn, error) {
			return newFakeICMP(answer(func(int) bool { return true }, pingable)), nil
		},
	}
}

func Test_Register_lists_host_status(t *testing.T) {
	session := connect(t, func(server *mcp.Server) { require.NoError(t, Register(server, testConfig)) })

	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, result.Tools, 1)
	tool := result.Tools[0]
	require.Equal(t, "host_status", tool.Name)
	require.Contains(t, tool.Description, "Only the listed hosts are covered")
	require.Contains(t, tool.Description, "not that the machine is off")
	require.True(t, tool.Annotations.ReadOnlyHint)
	require.True(t, tool.Annotations.IdempotentHint)
	require.True(t, *tool.Annotations.OpenWorldHint)
	hosts := tool.InputSchema.(map[string]any)["properties"].(map[string]any)["hosts"].(map[string]any)
	require.Equal(t, `The hosts to check, or ["all"] for every one: media (the media server); pi; nas.`, hosts["description"])
	require.Equal(t, []any{"all", "media", "pi", "nas"}, hosts["items"].(map[string]any)["enum"])
	require.EqualValues(t, 1, hosts["minItems"])
	require.NotNil(t, tool.OutputSchema)
}

func Test_Register_invalid_config(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	require.EqualError(t, Register(server, Config{}), "at least one host is required")
}

func Test_host_status_call(t *testing.T) {
	tests := map[string]struct {
		hosts  []any
		want   []string
		health []Health
		errMsg string
	}{
		"one":       {hosts: []any{"media"}, want: []string{"media"}, health: []Health{HealthUp}},
		"all":       {hosts: []any{"all"}, want: []string{"media", "pi", "NAS"}, health: []Health{HealthUp, HealthUnknown, HealthUnreachable}},
		"all-mixed": {hosts: []any{"pi", "all"}, want: []string{"media", "pi", "NAS"}, health: []Health{HealthUp, HealthUnknown, HealthUnreachable}},
		"dedupe":    {hosts: []any{"pi", "media", "pi"}, want: []string{"pi", "media"}, health: []Health{HealthUnknown, HealthUp}},
		"unknown":   {hosts: []any{"ghost"}, errMsg: "enum"},
		"empty":     {hosts: []any{}, errMsg: "minItems"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c := testChecker(t)
			session := connect(t, func(server *mcp.Server) { register(server, c, testConfig.choices()) })
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "host_status", Arguments: map[string]any{"hosts": tc.hosts}})
			if tc.errMsg != "" {
				if err == nil {
					require.True(t, result.IsError)
					require.Contains(t, result.Content[0].(*mcp.TextContent).Text, tc.errMsg)
					return
				}
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.False(t, result.IsError, result.Content)
			reports := result.StructuredContent.(map[string]any)["hosts"].([]any)
			require.Len(t, reports, len(tc.want))
			for i, r := range reports {
				report := r.(map[string]any)
				require.Equal(t, tc.want[i], report["host"])
				require.Equal(t, string(tc.health[i]), report["health"], report)
			}
		})
	}
}

func Test_checker_checkOne(t *testing.T) {
	c := testChecker(t)
	media := c.checkOne(context.Background(), c.hosts[0])
	require.Equal(t, HealthUp, media.Health)
	require.Equal(t, "every check passed", media.Summary)
	require.Equal(t, "127.0.0.1", media.Address)
	require.Equal(t, []string{"127.0.0.1"}, media.Addresses)
	require.Equal(t, "box-media.lan", media.Canonical)
	require.Equal(t, []string{"resolve", "ping", "ssh"}, []string{media.Checks[0].Name, media.Checks[1].Name, media.Checks[2].Name})
	require.Equal(t, Check{Name: "ssh", Outcome: OutcomePass, Detail: "OpenSSH_9.6"}, media.Checks[2])

	pi := c.checkOne(context.Background(), c.hosts[1])
	require.Equal(t, Report{Host: "pi", Health: HealthUnknown, Summary: "name does not exist in DNS", Checks: []Check{{Name: "resolve", Outcome: OutcomeFail, Detail: "name does not exist in DNS"}}}, pi)

	nas := c.checkOne(context.Background(), c.hosts[2])
	require.Equal(t, HealthUnreachable, nas.Health)
	require.Equal(t, "no probe answered from chatops-mcp", nas.Summary)
	require.Len(t, nas.Checks, 2)
}

func Test_checker_targets(t *testing.T) {
	c := &checker{hosts: testConfig.Hosts}
	_, err := c.targets(nil)
	require.EqualError(t, err, "hosts must name at least one host")
	_, err = c.targets([]string{"media", "ghost"})
	require.EqualError(t, err, `unknown host "ghost"`)
}

func Test_checker_handle_unknown_host(t *testing.T) {
	_, _, err := testChecker(t).handle(context.Background(), nil, Input{Hosts: []string{"ghost"}})
	require.EqualError(t, err, `unknown host "ghost"`)
}

func Test_checker_check_cancelled(t *testing.T) {
	c := testChecker(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.check(ctx, []string{"all"})
	require.ErrorIs(t, err, context.Canceled)
}

func Test_format(t *testing.T) {
	text := format([]Report{
		{Host: "media", Health: HealthDegraded, Summary: "ping passed; ssh failed", Checks: []Check{
			{Name: "resolve", Outcome: OutcomePass, Detail: "192.168.0.201"},
			{Name: "ping", Outcome: OutcomePass, Detail: "3/3 replies, average 1.2ms"},
			{Name: "ssh", Outcome: OutcomeFail, Detail: "connection refused", Responded: true},
		}},
		{Host: "pi", Health: HealthUnknown, Summary: "name does not exist in DNS", Checks: []Check{{Name: "resolve", Outcome: OutcomeFail, Detail: "name does not exist in DNS"}}},
	})
	require.Equal(t, `[DEGRADED] media - ping passed; ssh failed
  resolve PASS: 192.168.0.201
  ping PASS: 3/3 replies, average 1.2ms
  ssh FAIL: connection refused
[UNKNOWN] pi - name does not exist in DNS
  resolve FAIL: name does not exist in DNS`, text)
}

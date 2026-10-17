package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// packagedConfig is the directory the packages install as /etc/chatops/mcp-host.d.
const packagedConfig = "../../../package/systemd/mcp-host.d"

// writeDir writes files into a new config directory and returns it.
func writeDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	return dir
}

func hostYAML(name, address string) string {
	return "hosts:\n  - name: " + name + "\n    display: " + name + "\n    address: " + address + "\n"
}

func Test_Load(t *testing.T) {
	tests := map[string]struct {
		files  map[string]string
		want   []string
		errMsg string
	}{
		"file-name-order":  {files: map[string]string{"20-b.yaml": hostYAML("b", "b.lan"), "10-a.yaml": hostYAML("a", "10.0.0.1")}, want: []string{"a", "b"}},
		"empty-default":    {files: map[string]string{"00-default.yaml": "# hosts:\n", "10-a.yaml": hostYAML("a", "a.lan")}, want: []string{"a"}},
		"empty-dir":        {errMsg: "at least one host is required"},
		"duplicate-across": {files: map[string]string{"10-a.yaml": hostYAML("a", "a.lan"), "20-a.yaml": hostYAML("a", "b.lan")}, errMsg: `hosts[1]: duplicate name "a"`},
		"unknown-field":    {files: map[string]string{"10-a.yaml": hostYAML("a", "a.lan") + "    port: 22\n"}, errMsg: "field port not found"},
		"missing-dir":      {errMsg: "read host config directory"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir := writeDir(t, tc.files)
			if name == "missing-dir" {
				dir = filepath.Join(dir, "none.d")
			}
			cfg, err := Load(dir)
			if tc.errMsg != "" {
				require.ErrorContains(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			var names []string
			for _, h := range cfg.Hosts {
				names = append(names, h.Name)
			}
			require.Equal(t, tc.want, names)
		})
	}
}

func Test_Load_packaged(t *testing.T) {
	cfg, err := Load(packagedConfig)
	require.NoError(t, err)
	require.Equal(t, []Host{{Name: "localhost", Display: "localhost", Covers: []string{"this server", "the chatops host"}, Address: "localhost", Checks: []string{"ping"}}}, cfg.Hosts)
}

func Test_Config_validate(t *testing.T) {
	valid := Host{Name: "media", Display: "media", Address: "media.xiehang.com"}
	with := func(change func(*Host)) Host {
		h := valid
		change(&h)
		return h
	}
	tests := map[string]struct {
		hosts  []Host
		errMsg string
	}{
		"valid":            {hosts: []Host{valid, with(func(h *Host) { h.Name, h.Address, h.Checks, h.SSHPort = "pi", "192.168.0.200", []string{"ssh"}, 2222 })}},
		"ipv6-address":     {hosts: []Host{with(func(h *Host) { h.Address = "2001:db8::10" })}},
		"fqdn-dot":         {hosts: []Host{with(func(h *Host) { h.Address = "media.xiehang.com." })}},
		"ping-only":        {hosts: []Host{with(func(h *Host) { h.Checks = []string{"ping"} })}},
		"empty":            {errMsg: "at least one host is required"},
		"duplicate":        {hosts: []Host{valid, valid}, errMsg: `hosts[1]: duplicate name "media"`},
		"bad-name":         {hosts: []Host{with(func(h *Host) { h.Name = "Media" })}, errMsg: `hosts[0]: name "Media" must be lowercase letters, digits, and "-", and not "all"`},
		"reserved-name":    {hosts: []Host{with(func(h *Host) { h.Name = "all" })}, errMsg: `name "all" must be`},
		"no-display":       {hosts: []Host{with(func(h *Host) { h.Display = "" })}, errMsg: `host "media": display is required`},
		"no-address":       {hosts: []Host{with(func(h *Host) { h.Address = "" })}, errMsg: `host "media": address "" must be a DNS name or an IP address, without a scheme or port`},
		"url-address":      {hosts: []Host{with(func(h *Host) { h.Address = "https://media.xiehang.com" })}, errMsg: `address "https://media.xiehang.com" must be`},
		"port-address":     {hosts: []Host{with(func(h *Host) { h.Address = "media.xiehang.com:22" })}, errMsg: `address "media.xiehang.com:22" must be`},
		"hyphen-edge":      {hosts: []Host{with(func(h *Host) { h.Address = "-media.lan" })}, errMsg: `address "-media.lan" must be`},
		"too-long-address": {hosts: []Host{with(func(h *Host) { h.Address = strings.Repeat("a.", 127) + "aa" })}, errMsg: "must be a DNS name"},
		"bad-check":        {hosts: []Host{with(func(h *Host) { h.Checks = []string{"http"} })}, errMsg: `host "media": check "http" must be ping or ssh`},
		"repeated-check":   {hosts: []Host{with(func(h *Host) { h.Checks = []string{"ssh", "ssh"} })}, errMsg: `host "media": check "ssh" listed more than once`},
		"port-too-big":     {hosts: []Host{with(func(h *Host) { h.SSHPort = 70000 })}, errMsg: `host "media": ssh_port 70000 must be between 1 and 65535`},
		"port-negative":    {hosts: []Host{with(func(h *Host) { h.SSHPort = -1 })}, errMsg: "ssh_port -1 must be between"},
		"port-without-ssh": {hosts: []Host{with(func(h *Host) { h.Checks, h.SSHPort = []string{"ping"}, 22 })}, errMsg: `host "media": ssh_port is set but the ssh check is not enabled`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := Config{Hosts: tc.hosts}.validate()
			if tc.errMsg == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func Test_Host_enabledChecks(t *testing.T) {
	require.Equal(t, []string{"ping", "ssh"}, Host{}.enabledChecks())
	require.Equal(t, []string{"ping", "ssh"}, Host{Checks: []string{"ssh", "ping"}}.enabledChecks())
	require.Equal(t, []string{"ssh"}, Host{Checks: []string{"ssh"}}.enabledChecks())
}

func Test_Host_sshPort(t *testing.T) {
	require.Equal(t, 22, Host{}.sshPort())
	require.Equal(t, 2222, Host{SSHPort: 2222}.sshPort())
}

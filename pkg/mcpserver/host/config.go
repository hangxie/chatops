// Package host reports whether configured hosts are reachable: it resolves each name, pings it, and reads its SSH identification.
package host

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"github.com/hangxie/chatops/internal/confdir"
)

// Checks a host can enable; resolve always runs.
const (
	CheckPing = "ping"
	CheckSSH  = "ssh"
)

const (
	allHosts       = "all"
	defaultSSHPort = 22
)

var (
	hostName      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	dnsName       = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*\.?$`)
	defaultChecks = []string{CheckPing, CheckSSH}
)

// Config lists the hosts the host_status tool checks, in the order it reports them.
type Config struct {
	Hosts []Host `yaml:"hosts"`
}

// Host is one checkable machine.
type Host struct {
	Name    string `yaml:"name"`
	Display string `yaml:"display"`
	// Covers names what a user may call the host, so the model can map a question to it.
	Covers []string `yaml:"covers"`
	// Address is a DNS name or an IP address literal.
	Address string `yaml:"address"`
	// Checks enables ping and ssh; empty means both.
	Checks  []string `yaml:"checks"`
	SSHPort int      `yaml:"ssh_port"`
}

// Load merges the *.yaml files in a host config directory (see package confdir); unknown fields are errors.
func Load(dir string) (Config, error) {
	var cfg Config
	if err := confdir.Decode("host", dir, &cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("host config %s: %w", dir, err)
	}
	return cfg, nil
}

func (c Config) validate() error {
	if len(c.Hosts) == 0 {
		return errors.New("at least one host is required")
	}
	seen := map[string]bool{}
	for i, h := range c.Hosts {
		if err := h.validate(); err != nil {
			return fmt.Errorf("hosts[%d]: %w", i, err)
		}
		if seen[h.Name] {
			return fmt.Errorf("hosts[%d]: duplicate name %q", i, h.Name)
		}
		seen[h.Name] = true
	}
	return nil
}

func (h Host) validate() error {
	if !hostName.MatchString(h.Name) || h.Name == allHosts {
		return fmt.Errorf("name %q must be lowercase letters, digits, and \"-\", and not %q", h.Name, allHosts)
	}
	if h.Display == "" {
		return fmt.Errorf("host %q: display is required", h.Name)
	}
	if _, err := netip.ParseAddr(h.Address); err != nil && (len(h.Address) > 253 || !dnsName.MatchString(h.Address)) {
		return fmt.Errorf("host %q: address %q must be a DNS name or an IP address, without a scheme or port", h.Name, h.Address)
	}
	for i, check := range h.Checks {
		if check != CheckPing && check != CheckSSH {
			return fmt.Errorf("host %q: check %q must be %s or %s", h.Name, check, CheckPing, CheckSSH)
		}
		if slices.Contains(h.Checks[:i], check) {
			return fmt.Errorf("host %q: check %q listed more than once", h.Name, check)
		}
	}
	if h.SSHPort < 0 || h.SSHPort > 65535 {
		return fmt.Errorf("host %q: ssh_port %d must be between 1 and 65535", h.Name, h.SSHPort)
	}
	if h.SSHPort != 0 && !slices.Contains(h.enabledChecks(), CheckSSH) {
		return fmt.Errorf("host %q: ssh_port is set but the ssh check is not enabled", h.Name)
	}
	return nil
}

// enabledChecks returns the configured checks in a fixed order, or every check when none is listed.
func (h Host) enabledChecks() []string {
	if len(h.Checks) == 0 {
		return defaultChecks
	}
	var checks []string
	for _, check := range defaultChecks {
		if slices.Contains(h.Checks, check) {
			checks = append(checks, check)
		}
	}
	return checks
}

func (h Host) sshPort() int {
	if h.SSHPort == 0 {
		return defaultSSHPort
	}
	return h.SSHPort
}

// choices describes each host for the tool schema, with its address and what it covers.
func (c Config) choices() string {
	var choices []string
	for _, h := range c.Hosts {
		// The address lets the model tell an unlisted name from a listed host.
		choice := h.Name + " at " + h.Address
		if len(h.Covers) != 0 {
			choice += " (" + strings.Join(h.Covers, ", ") + ")"
		}
		choices = append(choices, choice)
	}
	return strings.Join(choices, "; ")
}

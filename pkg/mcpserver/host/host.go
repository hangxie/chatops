package host

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	checkTimeout       = 3 * time.Second
	maxConcurrentHosts = 4
)

// Input is the host_status tool's arguments.
type Input struct {
	Hosts []string `json:"hosts"`
}

// Output is the structured result of the host_status tool.
type Output struct {
	Hosts []Report `json:"hosts"`
}

// Report is what the checks observed about one host.
type Report struct {
	Host      string   `json:"host"`
	Health    Health   `json:"health" jsonschema:"up, degraded, unreachable, or unknown"`
	Summary   string   `json:"summary"`
	Address   string   `json:"address,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
	Canonical string   `json:"canonical,omitempty"`
	Checks    []Check  `json:"checks"`
}

type checker struct {
	hosts   []Host
	lookup  lookup
	dial    dialFunc
	listen  listenFunc
	timeout time.Duration
}

// Register adds the host_status tool, checking the hosts cfg lists, to server.
func Register(server *mcp.Server, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	c := &checker{hosts: cfg.Hosts, lookup: net.DefaultResolver, dial: (&net.Dialer{}).DialContext, listen: listenICMP, timeout: checkTimeout}
	register(server, c, cfg.choices())
	return nil
}

func register(server *mcp.Server, c *checker, choices string) {
	enum := []any{allHosts}
	for _, h := range c.hosts {
		enum = append(enum, h.Name)
	}
	openWorld := true
	mcp.AddTool(server, &mcp.Tool{
		Name:        "host_status",
		Description: "Report whether hosts are reachable from the ChatOps server: resolve each name, ping it, and read its SSH identification. Results state what was observed from here; unreachable means no probe answered, not that the machine is off. Use hosts [\"all\"] to check every host. Only the listed hosts are covered: for any other host, do not call this tool; say it is not covered.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"hosts": {
					Type:        "array",
					Description: "The hosts to check, or [\"all\"] for every one: " + choices + ".",
					Items:       &jsonschema.Schema{Type: "string", Enum: enum},
					MinItems:    jsonschema.Ptr(1),
				},
			},
			Required:             []string{"hosts"},
			AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		},
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  &openWorld,
		},
	}, c.handle)
}

func (c *checker) handle(ctx context.Context, _ *mcp.CallToolRequest, in Input) (*mcp.CallToolResult, Output, error) {
	reports, err := c.check(ctx, in.Hosts)
	if err != nil {
		return nil, Output{}, err
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: format(reports)}}}
	return result, Output{Hosts: reports}, nil
}

// targets maps the requested names to hosts in request order, dropping repeats; "all" means every host.
func (c *checker) targets(names []string) ([]Host, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("hosts must name at least one host")
	}
	if slices.Contains(names, allHosts) {
		return c.hosts, nil
	}
	var targets []Host
	for _, name := range names {
		i := slices.IndexFunc(c.hosts, func(h Host) bool { return h.Name == name })
		if i < 0 {
			return nil, fmt.Errorf("unknown host %q", name)
		}
		if !slices.ContainsFunc(targets, func(h Host) bool { return h.Name == name }) {
			targets = append(targets, c.hosts[i])
		}
	}
	return targets, nil
}

// check checks the targets concurrently, at most maxConcurrentHosts at a time, keeping request order.
func (c *checker) check(ctx context.Context, names []string) ([]Report, error) {
	targets, err := c.targets(names)
	if err != nil {
		return nil, err
	}
	reports := make([]Report, len(targets))
	slots := make(chan struct{}, maxConcurrentHosts)
	var wg sync.WaitGroup
	for i, h := range targets {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			reports[i] = c.checkOne(ctx, h)
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return reports, nil
}

// checkOne resolves the host, then runs its enabled probes concurrently against the selected address.
func (c *checker) checkOne(ctx context.Context, h Host) Report {
	res := resolve(ctx, c.lookup, c.timeout, h.Address)
	report := Report{Host: h.Display, Canonical: res.canonical, Checks: []Check{res.check}}
	if res.check.Outcome != OutcomePass {
		report.Health, report.Summary = HealthUnknown, res.check.Detail
		return report
	}
	report.Address = res.selected.String()
	for _, ip := range res.addresses {
		report.Addresses = append(report.Addresses, ip.String())
	}
	checks := h.enabledChecks()
	probes := make([]Check, len(checks))
	var wg sync.WaitGroup
	for i, name := range checks {
		wg.Go(func() {
			if name == CheckPing {
				probes[i] = checkPing(ctx, c.listen, c.timeout, res.selected)
				return
			}
			probes[i] = checkSSH(ctx, c.dial, c.timeout, res.selected, h.sshPort())
		})
	}
	wg.Wait()
	report.Checks = append(report.Checks, probes...)
	report.Health, report.Summary = classify(probes)
	return report
}

func format(reports []Report) string {
	var lines []string
	for _, r := range reports {
		lines = append(lines, fmt.Sprintf("[%s] %s - %s", strings.ToUpper(string(r.Health)), r.Host, r.Summary))
		for _, check := range r.Checks {
			lines = append(lines, fmt.Sprintf("  %s %s: %s", check.Name, strings.ToUpper(string(check.Outcome)), check.Detail))
		}
	}
	return strings.Join(lines, "\n")
}

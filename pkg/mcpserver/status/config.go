package status

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"

	"go.yaml.in/yaml/v3"
)

// Page formats a service's status page can use.
const (
	TypeStatuspage = "statuspage"
	TypeStatusIO   = "statusio"
	TypeSlack      = "slack"
	TypeGoogle     = "google"
)

var serviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Config lists the services the service_status tool checks, in the order it reports them.
type Config struct {
	Services []Service `yaml:"services"`
}

// Service is one checkable service and where its status page lives.
type Service struct {
	Name    string `yaml:"name"`
	Display string `yaml:"display"`
	// Covers names the products a user may ask about, so the model can map them to this service.
	Covers []string `yaml:"covers"`
	Type   string   `yaml:"type"`
	// URL is the status endpoint; every type but google uses it.
	URL string `yaml:"url"`
	// Feeds are the incident feeds a google service combines.
	Feeds []Feed `yaml:"feeds"`
}

// Feed is one Google incident feed, optionally narrowed to some product IDs.
type Feed struct {
	URL string `yaml:"url"`
	// Products keeps only incidents affecting these product IDs; empty keeps every incident.
	Products []string `yaml:"products"`
}

// Load reads and validates a status config file; unknown fields are errors.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read status config: %w", err)
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("parse status config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("status config %s: %w", path, err)
	}
	return cfg, nil
}

func (c Config) validate() error {
	if len(c.Services) == 0 {
		return errors.New("at least one service is required")
	}
	seen := map[string]bool{}
	for i, s := range c.Services {
		if err := s.validate(); err != nil {
			return fmt.Errorf("services[%d]: %w", i, err)
		}
		if seen[s.Name] {
			return fmt.Errorf("services[%d]: duplicate name %q", i, s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}

func (s Service) validate() error {
	if !serviceName.MatchString(s.Name) || s.Name == allServices {
		return fmt.Errorf("name %q must be lowercase letters, digits, and \"-\", and not %q", s.Name, allServices)
	}
	if s.Display == "" {
		return fmt.Errorf("service %q: display is required", s.Name)
	}
	switch s.Type {
	case TypeStatuspage, TypeStatusIO, TypeSlack:
		if len(s.Feeds) != 0 {
			return fmt.Errorf("service %q: feeds is only for type %q", s.Name, TypeGoogle)
		}
		return checkURL(s.Name, s.URL)
	case TypeGoogle:
		if s.URL != "" {
			return fmt.Errorf("service %q: type %q uses feeds, not url", s.Name, TypeGoogle)
		}
		if len(s.Feeds) == 0 {
			return fmt.Errorf("service %q: type %q needs at least one feed", s.Name, TypeGoogle)
		}
		for _, feed := range s.Feeds {
			if err := checkURL(s.Name, feed.URL); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("service %q: type %q must be %s, %s, %s, or %s", s.Name, s.Type, TypeStatuspage, TypeStatusIO, TypeSlack, TypeGoogle)
}

func checkURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("service %q: url %q must be an absolute http or https URL", name, raw)
	}
	return nil
}

// check builds the service's page reader; validate has already accepted its type.
func (s Service) check() checkFunc {
	switch s.Type {
	case TypeStatuspage:
		return statuspage(s.URL)
	case TypeStatusIO:
		return statusIO(s.URL)
	case TypeSlack:
		return slackStatus(s.URL)
	}
	return google(s.Feeds, s.Display)
}

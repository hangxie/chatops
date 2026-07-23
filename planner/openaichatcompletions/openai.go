// Package openaichatcompletions implements a planner.Planner backed by any service
// that speaks the OpenAI Chat Completions API — OpenAI itself, Google
// Gemini's OpenAI-compatible endpoint, a local Ollama, vLLM, LocalAI,
// and so on. The endpoint is therefore configurable through the URL.
//
// The package exports Scheme and Opener for wiring the planner into a
// planner.Registry under the "openai-chat-completions" URL scheme:
//
//	openai-chat-completions://api.openai.com/v1?model=gpt-5
//	openai-chat-completions://generativelanguage.googleapis.com/v1beta/openai?model=gemini-3.1-flash-lite
//	openai-chat-completions://localhost:11434/v1?insecure=true&keyless=true&model=llama3
//
// The host is required and locates the endpoint; its path defaults to
// "/v1", and the connection uses HTTPS unless insecure=true selects
// plain HTTP. The model query parameter is required.
// Unless keyless=true is explicit, the API key is resolved from the
// predefined planner credential and sent as a bearer token.
// Each message produces one request offering the enabled tools plus reply.
// Assistant prose and tool calls become plan steps; tool results are not fed
// back to the model, and the planner keeps no conversation history yet.
package openaichatcompletions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/internal/urlquery"
	"github.com/hangxie/chatops/planner"
	"github.com/hangxie/chatops/tool"
)

// Scheme is the URL scheme this planner serves in a planner.Registry.
// It names the OpenAI Chat Completions API specifically, leaving room
// for a future openai-responses backend.
const Scheme = "openai-chat-completions"

const (
	// defaultPath is the API path assumed for a host with no path.
	defaultPath = "/v1"
	// requestTimeout bounds one completion request.
	requestTimeout = 60 * time.Second
)

// systemPrompt tells the model to act via the offered functions only.
const systemPrompt = "You are a ChatOps planner. Decide how to handle the user's message. " +
	"To answer the user, ask a clarifying question, or acknowledge, call the reply function. " +
	"To carry out an operation, call the matching tool function. You may call several functions " +
	"in one turn. Use only the provided functions and do not invent tools."

// Opener parses the endpoint and model and resolves the planner API key.
func Opener(ctx context.Context, u *url.URL, creds cred.Store, tools *tool.Registry) (planner.Planner, error) {
	if u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("openai: URL %q must not carry userinfo, opaque data, or a fragment", u.String())
	}
	query := u.Query()
	wrapURL := func(err error) error {
		return fmt.Errorf("openai: URL %q: %w", u.String(), err)
	}
	if err := urlquery.Validate(query, "model", "insecure", "keyless"); err != nil {
		return nil, wrapURL(err)
	}
	model := strings.TrimSpace(query.Get("model"))
	if model == "" {
		return nil, fmt.Errorf("openai: URL %q is missing the required model query parameter", u.String())
	}

	insecure, err := urlquery.Bool(query, "insecure")
	if err != nil {
		return nil, wrapURL(err)
	}
	keyless, err := urlquery.Bool(query, "keyless")
	if err != nil {
		return nil, wrapURL(err)
	}
	baseURL, err := baseURLFromURL(u, insecure)
	if err != nil {
		return nil, err
	}

	apiKey, err := resolveAPIKey(ctx, creds, keyless)
	if err != nil {
		return nil, err
	}

	var schemes []string
	var descriptors map[string]tool.Descriptor
	if tools != nil {
		schemes = tools.Schemes()
		descriptors = make(map[string]tool.Descriptor, len(schemes))
		for _, scheme := range schemes {
			if d, ok := tools.Descriptor(scheme); ok {
				descriptors[scheme] = d
			}
		}
	}
	return Open(ctx, Config{BaseURL: baseURL, Model: model, APIKey: apiKey, ToolSchemes: schemes, ToolDescriptors: descriptors})
}

// baseURLFromURL builds the completion endpoint from u. The host is
// required — this planner targets any OpenAI-compatible endpoint, not a
// fixed provider — the path defaults to /v1, and insecure selects HTTP.
func baseURLFromURL(u *url.URL, insecure bool) (string, error) {
	if u.Host == "" {
		return "", fmt.Errorf("openai: URL %q must specify the endpoint host, e.g. openai-chat-completions://api.example.com/v1", u.String())
	}
	scheme := "https"
	if insecure {
		scheme = "http"
	}
	// EscapedPath avoids decoding a reserved "?" or "/" into the URL.
	path := u.EscapedPath()
	if path == "" {
		path = defaultPath
	}
	return scheme + "://" + u.Host + strings.TrimRight(path, "/"), nil
}

// normalizeBaseURL validates a directly-supplied base URL: an http(s)
// URL with a host and no query, fragment, or userinfo, trailing slash
// trimmed so the appended "/chat/completions" does not double up.
func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("openai: parse base URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("openai: base URL %q must use http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("openai: base URL %q has no host", raw)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("openai: base URL %q must not carry query, fragment, or userinfo", raw)
	}
	// EscapedPath avoids decoding a reserved "?" or "/" into the URL.
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

// resolveAPIKey retrieves the planner key unless keyless mode is explicit.
func resolveAPIKey(ctx context.Context, creds cred.Store, keyless bool) (string, error) {
	if keyless {
		return "", nil
	}
	key, err := cred.Require(ctx, creds, cred.PlannerAPIKey)
	if err != nil {
		if errors.Is(err, cred.ErrStoreNotConfigured) {
			return "", fmt.Errorf("openai: %w; use keyless=true for an unauthenticated endpoint", err)
		}
		return "", fmt.Errorf("openai: %w", err)
	}
	return key, nil
}

// Config holds the resolved settings for an openai planner.
type Config struct {
	// BaseURL is the endpoint base, without the /chat/completions suffix.
	BaseURL string
	// Model is the model identifier to request.
	Model string
	// APIKey is the bearer token; empty omits the Authorization header.
	APIKey string
	// ToolSchemes are the enabled operational tools offered to the model.
	ToolSchemes []string
	// ToolDescriptors holds the self-description, keyed by scheme, for the
	// enabled tools. Every scheme in ToolSchemes must have an entry here;
	// Open rejects a scheme without one. Each is offered a typed function.
	ToolDescriptors map[string]tool.Descriptor
}

// Planner is the OpenAI-compatible planner. It holds an HTTP client and
// is safe for concurrent use.
type Planner struct {
	client  *http.Client
	baseURL string
	model   string
	apiKey  string
	// defs is the function catalog offered to the model, built once at
	// Open from an immutable snapshot of the descriptors.
	defs []toolDef
	// funcs maps each offered function name to the tool scheme and action
	// it invokes, used to map the model's tool calls back to plan steps and
	// to reject calls to functions that were never offered.
	funcs map[string]toolFunc
}

// Open builds a planner from an already-resolved Config. Opener is the
// usual entry point; Open is exported for direct programmatic wiring
// and tests.
func Open(ctx context.Context, cfg Config) (*Planner, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("openai: empty base URL")
	}
	baseURL, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("openai: empty model")
	}
	if err := validateSchemes(cfg.ToolSchemes); err != nil {
		return nil, err
	}
	// Validate and deep-copy each descriptor so a caller mutating the config
	// after Open cannot change the catalog or race with Plan.
	descriptors := make(map[string]tool.Descriptor, len(cfg.ToolSchemes))
	for _, scheme := range cfg.ToolSchemes {
		d, ok := cfg.ToolDescriptors[scheme]
		if !ok {
			return nil, fmt.Errorf("openai: enabled tool %q has no descriptor", scheme)
		}
		if err := d.Validate(); err != nil {
			return nil, fmt.Errorf("openai: descriptor for tool %q is invalid: %w", scheme, err)
		}
		descriptors[scheme] = d.Clone()
	}
	// Build the catalog once; the schemas are serialized JSON, so nothing
	// reads the descriptors again after Open.
	defs, funcs, err := buildCatalog(cfg.ToolSchemes, descriptors)
	if err != nil {
		return nil, err
	}
	return &Planner{
		client:  &http.Client{Timeout: requestTimeout},
		baseURL: baseURL,
		model:   cfg.Model,
		apiKey:  cfg.APIKey,
		defs:    defs,
		funcs:   funcs,
	}, nil
}

// Plan makes one Chat Completions request for req.Text and maps the
// model's reply to plan steps. It returns an error when the request
// fails or the response contains no choices.
func (p *Planner) Plan(ctx context.Context, req planner.Request) (planner.Plan, error) {
	if err := ctx.Err(); err != nil {
		return planner.Plan{}, fmt.Errorf("openai: %w", err)
	}
	request := chatRequest{
		Model: p.model,
		Messages: []reqMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: req.Text},
		},
		Tools: p.defs,
	}
	response, err := chatComplete(ctx, p.client, p.baseURL, p.apiKey, request)
	if err != nil {
		return planner.Plan{}, err
	}
	if len(response.Choices) == 0 {
		return planner.Plan{}, fmt.Errorf("openai: completion returned no choices")
	}
	return stepsFromMessage(response.Choices[0].Message, p.funcs)
}

// Close releases nothing beyond the idle HTTP connections, which the
// transport reaps on its own.
func (p *Planner) Close() error {
	p.client.CloseIdleConnections()
	return nil
}

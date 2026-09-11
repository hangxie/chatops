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
//	openai-chat-completions://localhost:11434/v1?insecure=true&keyless=true&model=llama3&nothink=true
//
// The host is required and locates the endpoint; its path defaults to
// "/v1", and the connection uses HTTPS unless insecure=true selects
// plain HTTP. The model query parameter is required. nothink=true adds
// the request-level switches that turn a reasoning model's thinking
// phase off, for endpoints that accept them.
// Unless keyless=true is explicit, the API key is resolved from the
// predefined planner credential and sent as a bearer token.
// Each message produces one request offering the enabled tools, each as a
// function named for the tool and carrying the tool's own input schema
// downgraded to the subset completion endpoints accept. Assistant prose and
// tool calls become plan steps; tool results are not fed back to the model,
// and the planner keeps no conversation history yet.
package openaichatcompletions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hangxie/chatops/cred"
	"github.com/hangxie/chatops/internal/urlquery"
	"github.com/hangxie/chatops/planner"
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
// The trailing /no_think is the soft switch reasoning models such as
// Qwen3 honor to skip their thinking phase; models that do not
// recognize it read it as ordinary prose and ignore it.
const systemPrompt = "You are a ChatOps planner. Decide how to handle the user's message. " +
	"To answer the user, ask a clarifying question, or acknowledge, call the reply function. " +
	"To carry out an operation, call the matching tool function. You may call several functions " +
	"in one turn. Use only the provided functions and do not invent tools. /no_think"

// Opener parses the endpoint and model and resolves the planner API key.
func Opener(ctx context.Context, u *url.URL, creds cred.Store, tools planner.ToolSource) (planner.Planner, error) {
	if u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("openai: URL %q must not carry userinfo, opaque data, or a fragment", u.String())
	}
	query := u.Query()
	wrapURL := func(err error) error {
		return fmt.Errorf("openai: URL %q: %w", u.String(), err)
	}
	if err := urlquery.Validate(query, "model", "insecure", "keyless", "nothink"); err != nil {
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
	noThink, err := urlquery.Bool(query, "nothink")
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

	return Open(ctx, Config{BaseURL: baseURL, Model: model, APIKey: apiKey, NoThink: noThink, Tools: tools})
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
	// NoThink sends the request-level switches that disable a reasoning
	// model's thinking phase. Off by default: the switches are extra
	// request fields, which an endpoint that validates them strictly
	// rejects. The system prompt's /no_think always goes out regardless,
	// being ordinary prose.
	NoThink bool
	// Tools is the live catalog of tools offered to the model. It may be
	// nil, which offers none.
	Tools planner.ToolSource

	// Logger receives a record for any tool that cannot be offered to the
	// model. A nil Logger discards them.
	Logger *slog.Logger
}

// Planner is the OpenAI-compatible planner. It holds an HTTP client and
// is safe for concurrent use.
type Planner struct {
	client  *http.Client
	baseURL string
	model   string
	apiKey  string
	noThink bool
	logger  *slog.Logger
	tools   planner.ToolSource

	// The offered functions are derived from the tool catalog, which a
	// server may change at any time. They are rebuilt only when the
	// source's generation moves, so the usual request reuses them.
	mu         sync.Mutex
	cached     catalog
	cachedGen  uint64
	haveCached bool
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
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Planner{
		client:  &http.Client{Timeout: requestTimeout},
		baseURL: baseURL,
		model:   cfg.Model,
		apiKey:  cfg.APIKey,
		noThink: cfg.NoThink,
		logger:  logger,
		tools:   cfg.Tools,
	}, nil
}

// catalogFor returns the functions to offer, rebuilding them only when the
// tool source reports a different generation.
func (p *Planner) catalogFor(ctx context.Context) catalog {
	if p.tools == nil {
		return catalog{offered: map[string]bool{}}
	}
	generation := p.tools.Generation()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.haveCached && p.cachedGen == generation {
		return p.cached
	}
	p.cached = buildCatalog(p.tools.Tools(ctx), p.logger)
	p.cachedGen = generation
	p.haveCached = true
	return p.cached
}

// Plan makes one Chat Completions request for req.Text and maps the
// model's reply to plan steps. It returns an error when the request
// fails or the response contains no choices.
func (p *Planner) Plan(ctx context.Context, req planner.Request) (planner.Plan, error) {
	if err := ctx.Err(); err != nil {
		return planner.Plan{}, fmt.Errorf("openai: %w", err)
	}
	offer := p.catalogFor(ctx)
	request := chatRequest{
		Model: p.model,
		Messages: []reqMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: req.Text},
		},
		Tools: offer.defs,
	}
	if p.noThink {
		request = noThinkRequest(request)
	}
	response, err := chatComplete(ctx, p.client, p.baseURL, p.apiKey, request)
	if err != nil {
		return planner.Plan{}, err
	}
	if len(response.Choices) == 0 {
		return planner.Plan{}, fmt.Errorf("openai: completion returned no choices")
	}
	return stepsFromMessage(response.Choices[0].Message, offer.offered)
}

// Close releases nothing beyond the idle HTTP connections, which the
// transport reaps on its own.
func (p *Planner) Close() error {
	p.client.CloseIdleConnections()
	return nil
}

// Package llm provides the LLM API client for Xalgorix.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/config"
	"github.com/xalgord/xalgorix/v4/internal/proxy"
)

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// StreamChunk is a piece of streaming response.
type StreamChunk struct {
	Content string
	Done    bool
	Err     error
}

// Client is the LLM API client.
type Client struct {
	cfg          *config.Config
	httpClient   *http.Client
	proxyManager *proxy.Manager // nil when proxy is disabled
	apiModel     string
	provider     string // "openai", "anthropic", "google", "gemini", "deepseek", etc.
	mu           sync.Mutex
	totalIn      int
	totalOut     int
	// ctx is read concurrently by chatWithRetry / ChatStream and written by
	// SetContext. Use atomic.Value to avoid a race; loadCtx() is the only
	// reader, storeCtx() is the only writer.
	ctx atomic.Value // context.Context
}

// TokenUsage holds cumulative token counts.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// GetTokens returns cumulative token usage.
func (c *Client) GetTokens() (promptTokens, completionTokens, totalTokens int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalIn, c.totalOut, c.totalIn + c.totalOut
}

// buildHTTPClient creates an *http.Client.
// When proxies are configured it wires in a proxy-aware transport;
// otherwise it falls back to Go's default direct transport.
func buildHTTPClient(cfg *config.Config) (*http.Client, *proxy.Manager) {
	const timeout = 10 * time.Minute

	if !cfg.UseProxy {
		return &http.Client{Timeout: timeout}, nil
	}

	// Collect proxy strings: inline list takes precedence over file.
	var rawProxies []string
	if cfg.ProxyList != "" {
		for _, p := range strings.Split(cfg.ProxyList, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				rawProxies = append(rawProxies, p)
			}
		}
	}

	var mgr *proxy.Manager
	var err error

	if len(rawProxies) > 0 {
		mgr, err = proxy.New(rawProxies)
	} else if cfg.ProxyFile != "" {
		mgr, err = proxy.NewFromFile(cfg.ProxyFile)
	} else {
		log.Println("[llm] XALGORIX_USE_PROXY=true but no proxy list or file configured — using direct connection")
		return &http.Client{Timeout: timeout}, nil
	}

	if err != nil {
		log.Printf("[llm] Failed to load proxies: %v — falling back to direct connection", err)
		return &http.Client{Timeout: timeout}, nil
	}

	// Use the first proxy as static transport for the shared http.Client.
	// Per-request rotation is handled by httpClientForRequest().
	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: mgr.Transport(),
	}
	log.Printf("[llm] Proxy support enabled (%d proxies loaded, round-robin rotation)", mgr.Len())
	return httpClient, mgr
}

// httpClientForRequest returns an http.Client for a single request.
// When a proxy manager is configured it rotates to the next proxy;
// otherwise it reuses the shared client.
func (c *Client) httpClientForRequest() *http.Client {
	if c.proxyManager == nil {
		return c.httpClient
	}
	return &http.Client{
		Timeout:   c.httpClient.Timeout,
		Transport: c.proxyManager.Transport(),
	}
}

// NewClient creates a new LLM client.
func NewClient(cfg *config.Config) *Client {
	apiModel := cfg.ResolveModel()
	provider := ""
	if idx := strings.Index(apiModel, "/"); idx >= 0 {
		provider = strings.ToLower(apiModel[:idx])
	}
	httpClient, mgr := buildHTTPClient(cfg)
	c := &Client{
		cfg:          cfg,
		httpClient:   httpClient,
		proxyManager: mgr,
		apiModel:     apiModel,
		provider:     provider,
	}
	c.ctx.Store(ctxHolder{ctx: context.Background()})
	return c
}

// ctxHolder wraps context.Context so atomic.Value sees a concrete type even
// when callers pass a nil context.Context interface.
type ctxHolder struct{ ctx context.Context }

// SetContext sets the context for HTTP requests, enabling cancellation.
// Safe for concurrent use.
func (c *Client) SetContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.ctx.Store(ctxHolder{ctx: ctx})
}

// loadCtx returns the current request context, falling back to Background
// if SetContext has never been called.
func (c *Client) loadCtx() context.Context {
	if v := c.ctx.Load(); v != nil {
		if h, ok := v.(ctxHolder); ok && h.ctx != nil {
			return h.ctx
		}
	}
	return context.Background()
}

// chatRequest is the OpenAI-compatible chat completion request.
type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	Temperature   float64        `json:"temperature,omitempty"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
}

// streamOptions opts into usage stats for OpenAI-compatible streaming
// responses (OpenAI, Groq, DeepSeek, MiniMax, etc.). Without this the
// final `usage` field is omitted from the SSE stream.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatChoice represents a response choice.
type chatChoice struct {
	Delta   struct{ Content string } `json:"delta"`
	Message struct{ Content string } `json:"message"`
}

// chatResponse is the OpenAI-compatible respo
// Package proxy provides HTTP/HTTPS/SOCKS5 proxy support for Xalgorix.
// It handles parsing, building and rotating proxies transparently.
package proxy

import (
	"bufio"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// Proxy holds a parsed proxy entry.
type Proxy struct {
	Raw      string // original string as provided
	ProxyURL *url.URL
}

// Manager holds a list of proxies and rotates through them.
type Manager struct {
	proxies []*Proxy
	counter uint64
	mu      sync.RWMutex
}

// New creates a Manager from a slice of raw proxy strings.
// Accepted formats:
//
//	"ip:port"                   → HTTP proxy, no auth
//	"ip:port:user:pass"         → HTTP proxy, with auth
//	"socks5://ip:port"          → SOCKS5 proxy, no auth
//	"socks5://user:pass@ip:port" → SOCKS5 proxy, with auth
func New(raw []string) (*Manager, error) {
	m := &Manager{}
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		p, err := parse(s)
		if err != nil {
			log.Printf("[proxy] Skipping invalid proxy %q: %v", s, err)
			continue
		}
		m.proxies = append(m.proxies, p)
	}
	if len(m.proxies) == 0 {
		return nil, fmt.Errorf("no valid proxies found")
	}
	log.Printf("[proxy] Loaded %d proxies", len(m.proxies))
	return m, nil
}

// NewFromFile loads proxies from a text file (one proxy per line).
func NewFromFile(path string) (*Manager, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open proxy file %s: %w", path, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading proxy file: %w", err)
	}
	return New(lines)
}

// parse converts a raw string into a Proxy.
func parse(raw string) (*Proxy, error) {
	// Already a full URL (has scheme)
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL: %w", err)
		}
		// Normalise scheme
		scheme := strings.ToLower(u.Scheme)
		if scheme != "http" && scheme != "https" && scheme != "socks5" {
			return nil, fmt.Errorf("unsupported proxy scheme %q (use http, https or socks5)", u.Scheme)
		}
		return &Proxy{Raw: raw, ProxyURL: u}, nil
	}

	// Plain format: ip:port  or  ip:port:user:pass
	parts := strings.Split(raw, ":")
	switch len(parts) {
	case 2:
		// ip:port — HTTP proxy, no auth
		u := &url.URL{
			Scheme: "http",
			Host:   parts[0] + ":" + parts[1],
		}
		return &Proxy{Raw: raw, ProxyURL: u}, nil
	case 4:
		// ip:port:user:pass — HTTP proxy with auth
		u := &url.URL{
			Scheme:   "http",
			Host:     parts[0] + ":" + parts[1],
			User:     url.UserPassword(parts[2], parts[3]),
		}
		return &Proxy{Raw: raw, ProxyURL: u}, nil
	default:
		return nil, fmt.Errorf("expected ip:port or ip:port:user:pass, got %d segments", len(parts))
	}
}

// Next returns the next proxy using round-robin rotation.
func (m *Manager) Next() *Proxy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.proxies) == 0 {
		return nil
	}
	idx := atomic.AddUint64(&m.counter, 1) - 1
	return m.proxies[int(idx)%len(m.proxies)]
}

// Random returns a random proxy from the list.
func (m *Manager) Random() *Proxy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.proxies) == 0 {
		return nil
	}
	return m.proxies[rand.Intn(len(m.proxies))]
}

// Len returns the number of loaded proxies.
func (m *Manager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.proxies)
}

// Transport returns an *http.Transport that routes through the next proxy
// in the rotation. The transport is ready to be assigned to http.Client.Transport.
func (m *Manager) Transport() http.RoundTripper {
	proxy := m.Next()
	if proxy == nil {
		return http.DefaultTransport
	}
	log.Printf("[proxy] Using proxy: %s", proxy.ProxyURL.Redacted())
	return &http.Transport{
		Proxy: http.ProxyURL(proxy.ProxyURL),
		// Keep sane defaults from http.DefaultTransport
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        100,
		IdleConnTimeout:     90e9, // 90s in nanoseconds
		TLSHandshakeTimeout: 10e9, // 10s
	}
}

// StaticTransport returns a fixed *http.Transport using the given proxy URL.
// Useful when you want to pin a single proxy for the lifetime of an http.Client.
func StaticTransport(proxyURL *url.URL) http.RoundTripper {
	log.Printf("[proxy] Static proxy: %s", proxyURL.Redacted())
	return &http.Transport{
		Proxy:               http.ProxyURL(proxyURL),
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        100,
		IdleConnTimeout:     90e9,
		TLSHandshakeTimeout: 10e9,
	}
}

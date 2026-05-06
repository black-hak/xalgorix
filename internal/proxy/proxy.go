// Package proxy provides HTTP, HTTPS and SOCKS5 proxy support for Xalgorix.
// It supports the following formats:
//   - ip:port
//   - ip:port:user:pass
//   - socks5://ip:port
//   - socks5://user:pass@ip:port
package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ProxyType represents the type of proxy.
type ProxyType string

const (
	ProxyTypeHTTP   ProxyType = "http"
	ProxyTypeHTTPS  ProxyType = "https"
	ProxyTypeSOCKS5 ProxyType = "socks5"
)

// Proxy holds the parsed proxy configuration.
type Proxy struct {
	Raw      string
	Type     ProxyType
	Host     string
	Port     string
	Username string
	Password string
}

// URL returns the proxy as a *url.URL suitable for http.Transport.
func (p *Proxy) URL() (*url.URL, error) {
	var scheme string
	switch p.Type {
	case ProxyTypeSOCKS5:
		scheme = "socks5"
	default:
		scheme = "http"
	}

	raw := fmt.Sprintf("%s://%s:%s", scheme, p.Host, p.Port)
	if p.Username != "" {
		raw = fmt.Sprintf("%s://%s:%s@%s:%s", scheme, url.QueryEscape(p.Username), url.QueryEscape(p.Password), p.Host, p.Port)
	}
	return url.Parse(raw)
}

// String returns a human-readable representation (password masked).
func (p *Proxy) String() string {
	if p.Username != "" {
		return fmt.Sprintf("%s://%s:****@%s:%s", p.Type, p.Username, p.Host, p.Port)
	}
	return fmt.Sprintf("%s://%s:%s", p.Type, p.Host, p.Port)
}

// Parse parses a proxy string into a Proxy struct.
// Supported formats:
//   - ip:port                     → HTTP proxy, no auth
//   - ip:port:user:pass           → HTTP proxy, with auth
//   - socks5://ip:port            → SOCKS5, no auth
//   - socks5://user:pass@ip:port  → SOCKS5, with auth
//   - http://ip:port              → HTTP proxy, no auth
//   - http://user:pass@ip:port    → HTTP proxy, with auth
func Parse(raw string) (*Proxy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("proxy: empty string")
	}

	// Detect URI scheme
	if strings.Contains(raw, "://") {
		return parseURI(raw)
	}

	// Plain format: ip:port or ip:port:user:pass
	return parsePlain(raw)
}

func parseURI(raw string) (*Proxy, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("proxy: invalid URI %q: %w", raw, err)
	}

	p := &Proxy{Raw: raw}

	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h":
		p.Type = ProxyTypeSOCKS5
	case "https":
		p.Type = ProxyTypeHTTPS
	default:
		p.Type = ProxyTypeHTTP
	}

	p.Host = u.Hostname()
	p.Port = u.Port()
	if p.Port == "" {
		if p.Type == ProxyTypeSOCKS5 {
			p.Port = "1080"
		} else {
			p.Port = "8080"
		}
	}

	if u.User != nil {
		p.Username = u.User.Username()
		p.Password, _ = u.User.Password()
	}

	if p.Host == "" {
		return nil, fmt.Errorf("proxy: missing host in %q", raw)
	}

	return p, nil
}

func parsePlain(raw string) (*Proxy, error) {
	parts := strings.Split(raw, ":")
	switch len(parts) {
	case 2:
		// ip:port
		return &Proxy{
			Raw:  raw,
			Type: ProxyTypeHTTP,
			Host: parts[0],
			Port: parts[1],
		}, nil
	case 4:
		// ip:port:user:pass
		return &Proxy{
			Raw:      raw,
			Type:     ProxyTypeHTTP,
			Host:     parts[0],
			Port:     parts[1],
			Username: parts[2],
			Password: parts[3],
		}, nil
	default:
		return nil, fmt.Errorf("proxy: unrecognised format %q (want ip:port or ip:port:user:pass)", raw)
	}
}

// NewTransport creates an *http.Transport configured to use the given proxy.
// Returns the default transport if p is nil.
func NewTransport(p *Proxy) (*http.Transport, error) {
	if p == nil {
		return &http.Transport{}, nil
	}
	proxyURL, err := p.URL()
	if err != nil {
		return nil, err
	}
	return &http.Transport{
		Proxy:               http.ProxyURL(proxyURL),
		DisableKeepAlives:   false,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}, nil
}

// NewClient returns an *http.Client configured with the given proxy and timeout.
func NewClient(p *Proxy, timeout time.Duration) (*http.Client, error) {
	tr, err := NewTransport(p)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}, nil
}

// ---------------------------------------------------------------------------
// Pool — proxy rotation
// ---------------------------------------------------------------------------

// Pool manages a list of proxies and provides round-robin / random rotation.
type Pool struct {
	mu      sync.Mutex
	proxies []*Proxy
	index   int
	rng     *rand.Rand
}

// NewPool creates a Pool from a slice of raw proxy strings.
// Invalid entries are skipped with a warning printed to stderr.
func NewPool(rawList []string) *Pool {
	p := &Pool{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	for _, raw := range rawList {
		prx, err := Parse(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[proxy] skipping invalid entry %q: %v\n", raw, err)
			continue
		}
		p.proxies = append(p.proxies, prx)
	}
	return p
}

// LoadFile reads a proxy list from a file (one proxy per line, # comments ignored).
func LoadFile(path string) (*Pool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("proxy: cannot open file %q: %w", path, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("proxy: error reading file: %w", err)
	}
	return NewPool(lines), nil
}

// Len returns the number of valid proxies in the pool.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.proxies)
}

// Next returns the next proxy using round-robin rotation.
// Returns nil if the pool is empty.
func (p *Pool) Next() *Proxy {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.proxies) == 0 {
		return nil
	}
	prx := p.proxies[p.index%len(p.proxies)]
	p.index++
	return prx
}

// Random returns a random proxy from the pool.
// Returns nil if the pool is empty.
func (p *Pool) Random() *Proxy {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.proxies) == 0 {
		return nil
	}
	return p.proxies[p.rng.Intn(len(p.proxies))]
}

// All returns a copy of all proxies in the pool.
func (p *Pool) All() []*Proxy {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*Proxy, len(p.proxies))
	copy(out, p.proxies)
	return out
}

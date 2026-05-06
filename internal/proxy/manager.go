package proxy

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// Manager is the central proxy manager used by the rest of the application.
// It is initialised once via Init() and then accessed through the package-level
// helpers GetClient() / GetProxy().
type Manager struct {
	enabled  bool
	pool     *Pool
	rotation string // "roundrobin" | "random"
	timeout  time.Duration
}

var defaultManager *Manager

// Init initialises the package-level Manager from explicit parameters.
// Call this once at startup (e.g. from main or server init).
//
//	  useProxy   – enable proxy routing
//	  proxyURL   – single proxy string (takes precedence over proxyFile)
//	  proxyFile  – path to a file with one proxy per line
//	  rotation   – "roundrobin" or "random"
//	  timeout    – per-request timeout
func Init(useProxy bool, proxyURL, proxyFile, rotation string, timeout time.Duration) error {
	m := &Manager{
		enabled:  useProxy,
		rotation: rotation,
		timeout:  timeout,
	}

	if useProxy {
		switch {
		case proxyURL != "":
			m.pool = NewPool([]string{proxyURL})
		case proxyFile != "":
			pool, err := LoadFile(proxyFile)
			if err != nil {
				return fmt.Errorf("proxy manager: %w", err)
			}
			m.pool = pool
		default:
			fmt.Fprintf(os.Stderr, "[proxy] USE_PROXY=true but no PROXY_URL or PROXY_FILE set — running without proxy\n")
			m.enabled = false
		}

		if m.pool != nil && m.pool.Len() == 0 {
			fmt.Fprintf(os.Stderr, "[proxy] proxy list is empty — running without proxy\n")
			m.enabled = false
		}
	}

	defaultManager = m
	return nil
}

// Enabled reports whether proxy routing is active.
func Enabled() bool {
	if defaultManager == nil {
		return false
	}
	return defaultManager.enabled
}

// GetProxy returns the next proxy according to the configured rotation strategy.
// Returns nil when proxy routing is disabled or the pool is empty.
func GetProxy() *Proxy {
	if defaultManager == nil || !defaultManager.enabled || defaultManager.pool == nil {
		return nil
	}
	if defaultManager.rotation == "random" {
		return defaultManager.pool.Random()
	}
	return defaultManager.pool.Next()
}

// GetClient returns an *http.Client wired to the next proxy in the pool.
// When proxy routing is disabled it returns a plain client with the configured timeout.
func GetClient() (*http.Client, error) {
	p := GetProxy()
	return NewClient(p, defaultManager.timeoutOrDefault())
}

// GetClientFor returns an *http.Client wired to the given proxy string.
// Useful when the caller wants to pin a specific proxy for a request sequence.
func GetClientFor(rawProxy string) (*http.Client, error) {
	p, err := Parse(rawProxy)
	if err != nil {
		return nil, err
	}
	return NewClient(p, defaultManager.timeoutOrDefault())
}

func (m *Manager) timeoutOrDefault() time.Duration {
	if m == nil || m.timeout == 0 {
		return 30 * time.Second
	}
	return m.timeout
}

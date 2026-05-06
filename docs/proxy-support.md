# Proxy Support

Xalgorix supports routing all outbound network requests through **HTTP**, **HTTPS**, and **SOCKS5** proxies, with automatic rotation from a proxy list.

## Quick Start

### Single proxy (environment variable)

```bash
# HTTP / HTTPS proxy — no auth
export XALGORIX_USE_PROXY=true
export XALGORIX_PROXY_URL="1.2.3.4:8080"

# HTTP / HTTPS proxy — with auth
export XALGORIX_USE_PROXY=true
export XALGORIX_PROXY_URL="1.2.3.4:8080:username:password"

# SOCKS5 proxy
export XALGORIX_USE_PROXY=true
export XALGORIX_PROXY_URL="socks5://10.0.0.1:1080"

# SOCKS5 with auth
export XALGORIX_USE_PROXY=true
export XALGORIX_PROXY_URL="socks5://user:pass@10.0.0.1:1080"
```

### Proxy list with rotation

```bash
export XALGORIX_USE_PROXY=true
export XALGORIX_PROXY_FILE="/path/to/proxies.txt"
export XALGORIX_PROXY_ROTATION="roundrobin"   # or "random"
```

See `proxies.txt.example` for the file format.

## Configuration Reference

| Environment Variable      | Default        | Description                                                  |
|---------------------------|----------------|--------------------------------------------------------------|
| `XALGORIX_USE_PROXY`      | `false`        | Set to `true` to enable proxy routing                        |
| `XALGORIX_PROXY_URL`      | _(empty)_      | Single proxy string; takes precedence over `PROXY_FILE`      |
| `XALGORIX_PROXY_FILE`     | _(empty)_      | Path to a file with one proxy per line                       |
| `XALGORIX_PROXY_ROTATION` | `roundrobin`   | Rotation strategy: `roundrobin` or `random`                  |

All variables can also be placed in `~/.xalgorix.env`.

## Proxy String Formats

| Format                           | Type   | Auth |
|----------------------------------|--------|------|
| `ip:port`                        | HTTP   | No   |
| `ip:port:user:pass`              | HTTP   | Yes  |
| `socks5://ip:port`               | SOCKS5 | No   |
| `socks5://user:pass@ip:port`     | SOCKS5 | Yes  |
| `http://ip:port`                 | HTTP   | No   |
| `http://user:pass@ip:port`       | HTTP   | Yes  |

## How It Works

1. At startup, `proxy.Init()` is called with the values from `config.Config`.
2. Every call to `proxy.GetClient()` returns an `*http.Client` pre-configured with the next proxy in the pool.
3. If the pool is empty or `USE_PROXY=false`, a plain client is returned — **zero change in behaviour for existing users**.
4. The `Pool` is goroutine-safe; rotation state is protected by a mutex.

## Adding Proxy Support to New Code

```go
import "github.com/xalgord/xalgorix/v4/internal/proxy"

// Get a client for the next proxy in the rotation
client, err := proxy.GetClient()
if err != nil {
    return err
}
resp, err := client.Get(targetURL)
```

Or pin a specific proxy:

```go
client, err := proxy.GetClientFor("socks5://10.0.0.1:1080")
```

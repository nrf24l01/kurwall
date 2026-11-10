// Package docker maps container IP addresses to container identities using the
// local Docker Engine API socket.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

// Container is the identity of a running container.
type Container struct {
	ID      string
	Name    string
	Network string
}

// Tracker periodically refreshes the container IP map.
type Tracker struct {
	socket   string
	client   *http.Client
	logger   zerolog.Logger
	enabled  bool
	mu       sync.RWMutex
	byIP     map[netip.Addr]Container
	lastCall atomic.Int64
}

// New creates a Docker tracker. It never fails; unreachable Docker is logged
// and container enrichment is simply reported as unavailable.
func New(socket string, enabled bool, logger zerolog.Logger) *Tracker {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    2,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Tracker{
		socket:  socket,
		enabled: enabled,
		client:  &http.Client{Transport: transport, Timeout: 3 * time.Second},
		logger:  logger,
		byIP:    map[netip.Addr]Container{},
	}
}

// Run refreshes the container map until ctx is cancelled.
func (t *Tracker) Run(ctx context.Context, interval time.Duration) {
	if !t.enabled {
		return
	}
	if err := t.Refresh(ctx); err != nil {
		t.logger.Warn().Err(err).Str("socket", t.socket).Msg("docker container tracking unavailable")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := t.Refresh(ctx); err != nil {
				t.logger.Debug().Err(err).Msg("docker refresh failed")
			}
		}
	}
}

// Refresh reloads the running container list.
func (t *Tracker) Refresh(ctx context.Context) error {
	if !t.enabled {
		return nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://docker/containers/json", nil)
	if err != nil {
		return err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("query docker: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker returned status %d", resp.StatusCode)
	}
	var containers []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		State string   `json:"State"`
		Net   struct {
			Networks map[string]struct {
				IPAddress         string `json:"IPAddress"`
				GlobalIPv6Address string `json:"GlobalIPv6Address"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return fmt.Errorf("decode docker containers: %w", err)
	}
	next := make(map[netip.Addr]Container, len(containers)*2)
	for _, c := range containers {
		name := strings.TrimPrefix(firstName(c.Names), "/")
		for network, settings := range c.Net.Networks {
			container := Container{ID: c.ID, Name: name, Network: network}
			if settings.IPAddress != "" {
				if addr, err := netip.ParseAddr(settings.IPAddress); err == nil {
					next[addr.Unmap()] = container
				}
			}
			if settings.GlobalIPv6Address != "" {
				if addr, err := netip.ParseAddr(settings.GlobalIPv6Address); err == nil {
					next[addr.Unmap()] = container
				}
			}
		}
	}
	t.mu.Lock()
	t.byIP = next
	t.mu.Unlock()
	return nil
}

func firstName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// Lookup resolves a container by IP. Unknown IPs trigger at most one refresh
// every two seconds, in the background.
func (t *Tracker) Lookup(ip netip.Addr) (Container, bool) {
	t.mu.RLock()
	container, ok := t.byIP[ip.Unmap()]
	t.mu.RUnlock()
	if ok || !t.enabled {
		return container, ok
	}
	now := time.Now().UnixNano()
	last := t.lastCall.Load()
	if now-last > int64(2*time.Second) && t.lastCall.CompareAndSwap(last, now) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = t.Refresh(ctx)
		}()
	}
	return Container{}, false
}

// IsContainer reports whether an IP belongs to a known container.
func (t *Tracker) IsContainer(ip netip.Addr) bool {
	_, ok := t.Lookup(ip)
	return ok
}

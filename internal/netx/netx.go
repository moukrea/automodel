// Package netx dials with a fallback to the addresses a host last resolved
// to. On some machines every uncached lookup takes 5 s or more (a VPN's
// resolver, an AAAA answer that never comes back): a Jev call bounded to a
// few seconds then times out before its request is even sent. The last good
// addresses of each host are kept in a small file, and a dial whose lookup
// is slow goes to them.
package netx

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MaxAge bounds how old cached addresses may be to stand in for a lookup.
const MaxAge = 7 * 24 * time.Hour

// Dialer resolves with Lookup and dials with Dialer. When the lookup takes
// longer than Wait and the cache has addresses for the host, it dials those.
type Dialer struct {
	Cache  string        // JSON file of host → addresses ("" disables it)
	Wait   time.Duration // default 300ms
	Lookup func(ctx context.Context, host string) ([]string, error)
	Dialer net.Dialer
	mu     sync.Mutex
}

type entry struct {
	Addrs []string  `json:"addrs"`
	At    time.Time `json:"at"`
}

func (d *Dialer) lookup(ctx context.Context, host string) ([]string, error) {
	if d.Lookup != nil {
		return d.Lookup(ctx, host)
	}
	return net.DefaultResolver.LookupHost(ctx, host)
}

type result struct {
	addrs []string
	err   error
}

// DialContext is a net.Dialer.DialContext for http.Transport.
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) != nil || d.Cache == "" {
		return d.Dialer.DialContext(ctx, network, addr)
	}
	// The lookup outlives the dial: a late answer still refreshes the cache.
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	ch := make(chan result, 1)
	go func() {
		defer cancel()
		a, err := d.lookup(lctx, host)
		if err == nil && len(a) > 0 {
			d.save(host, a)
		}
		ch <- result{a, err}
	}()
	wait := d.Wait
	if wait <= 0 {
		wait = 300 * time.Millisecond
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case r := <-ch:
		if r.err == nil {
			return d.dialAny(ctx, network, r.addrs, port)
		}
		if c, cerr := d.dialAny(ctx, network, d.load(host), port); cerr == nil {
			return c, nil
		}
		return nil, r.err
	case <-t.C:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if cached := d.load(host); len(cached) > 0 {
		if c, err := d.dialAny(ctx, network, cached, port); err == nil {
			return c, nil
		}
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return d.dialAny(ctx, network, r.addrs, port)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *Dialer) dialAny(ctx context.Context, network string, addrs []string, port string) (net.Conn, error) {
	err := errors.New("no address")
	for _, a := range addrs {
		c, e := d.Dialer.DialContext(ctx, network, net.JoinHostPort(a, port))
		if e == nil {
			return c, nil
		}
		err = e
		if ctx.Err() != nil {
			break
		}
	}
	return nil, err
}

func (d *Dialer) read() map[string]entry {
	m := map[string]entry{}
	if b, err := os.ReadFile(d.Cache); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (d *Dialer) load(host string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.read()[host]
	if !ok || time.Since(e.At) > MaxAge {
		return nil
	}
	return e.Addrs
}

func (d *Dialer) save(host string, addrs []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.read()
	m[host] = entry{Addrs: addrs, At: time.Now()}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(d.Cache), ".dns-*")
	if err != nil {
		return
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return
	}
	tmp.Close()
	if os.Rename(tmp.Name(), d.Cache) != nil {
		os.Remove(tmp.Name())
	}
}

// Refresh resolves hosts every interval until ctx ends, which keeps both
// the cache file and the system resolver's cache warm (the proxy runs it:
// hooks are short-lived processes that would otherwise often pay for a cold
// lookup).
func (d *Dialer) Refresh(ctx context.Context, hosts []string, every time.Duration) {
	for {
		for _, h := range hosts {
			lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if a, err := d.lookup(lctx, h); err == nil && len(a) > 0 {
				d.save(h, a)
			}
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

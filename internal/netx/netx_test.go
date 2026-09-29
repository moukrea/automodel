package netx

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func listener(t *testing.T) (port string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return port
}

func TestSlowLookupUsesCache(t *testing.T) {
	port := listener(t)
	d := &Dialer{Cache: filepath.Join(t.TempDir(), "dns.json"), Wait: 50 * time.Millisecond}
	d.save("jev.example", []string{"127.0.0.1"})
	d.Lookup = func(ctx context.Context, host string) ([]string, error) {
		select {
		case <-time.After(5 * time.Second):
			return []string{"127.0.0.1"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	start := time.Now()
	c, err := d.DialContext(context.Background(), "tcp", "jev.example:"+port)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if el := time.Since(start); el > time.Second {
		t.Errorf("dial took %v: the cached address wasn't used", el)
	}
}

func TestFastLookupIsSaved(t *testing.T) {
	port := listener(t)
	d := &Dialer{Cache: filepath.Join(t.TempDir(), "dns.json")}
	d.Lookup = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	c, err := d.DialContext(context.Background(), "tcp", "jev.example:"+port)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if got := d.load("jev.example"); len(got) != 1 || got[0] != "127.0.0.1" {
		t.Errorf("cache = %v", got)
	}
}

func TestFailedLookupFallsBackToCache(t *testing.T) {
	port := listener(t)
	d := &Dialer{Cache: filepath.Join(t.TempDir(), "dns.json")}
	d.save("jev.example", []string{"127.0.0.1"})
	d.Lookup = func(context.Context, string) ([]string, error) { return nil, errors.New("servfail") }
	c, err := d.DialContext(context.Background(), "tcp", "jev.example:"+port)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestNoCacheWaitsForTheLookup(t *testing.T) {
	port := listener(t)
	d := &Dialer{Cache: filepath.Join(t.TempDir(), "dns.json"), Wait: 10 * time.Millisecond}
	d.Lookup = func(context.Context, string) ([]string, error) {
		time.Sleep(100 * time.Millisecond)
		return []string{"127.0.0.1"}, nil
	}
	c, err := d.DialContext(context.Background(), "tcp", "jev.example:"+port)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

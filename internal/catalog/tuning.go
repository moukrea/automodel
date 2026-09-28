package catalog

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Tuning: the catalog automodel routes with is either the default one,
// shipped in the binary and updated with every release, or a custom one:
// the user's file. A partial file (only the keys the user changes) is
// layered over the default, key by key (arrays such as [[measurements]] are
// replaced whole); a whole catalog (it sets meta.schema) is used as it is.

// Shipped is the default catalog, set by the binary at startup (the
// automodel package embeds it). Empty in tests that don't set it.
var Shipped []byte

// IsWhole reports whether data is a whole catalog (it sets meta.schema)
// rather than a partial tuning file.
func IsWhole(data []byte) bool {
	var probe struct {
		Meta struct {
			Schema int `toml:"schema"`
		} `toml:"meta"`
	}
	_, err := toml.Decode(string(data), &probe)
	return err == nil && probe.Meta.Schema != 0
}

// Merge layers over on top of base and returns the merged TOML.
func Merge(base, over []byte) ([]byte, error) {
	var a, b map[string]any
	if _, err := toml.Decode(string(base), &a); err != nil {
		return nil, fmt.Errorf("default catalog: %w", err)
	}
	if _, err := toml.Decode(string(over), &b); err != nil {
		return nil, err
	}
	if a == nil {
		a = map[string]any{}
	}
	deepMerge(a, b)
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(a); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMerge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

// Override is one key a custom file sets, with the default's value.
type Override struct {
	Key            string
	Value, Default any
	New            bool // not in the default catalog
}

// Overrides lists the leaf keys over sets and how they differ from base
// (keys equal to the default are left out).
func Overrides(base, over []byte) ([]Override, error) {
	var a, b map[string]any
	if _, err := toml.Decode(string(base), &a); err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(over), &b); err != nil {
		return nil, err
	}
	var out []Override
	var walk func(prefix string, d, s map[string]any)
	walk = func(prefix string, d, s map[string]any) {
		for k, v := range s {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			if sm, ok := v.(map[string]any); ok {
				dm, _ := d[k].(map[string]any)
				if dm == nil {
					dm = map[string]any{}
				}
				walk(key, dm, sm)
				continue
			}
			dv, had := d[k]
			if had && fmt.Sprint(dv) == fmt.Sprint(v) {
				continue
			}
			out = append(out, Override{Key: key, Value: v, Default: dv, New: !had})
		}
	}
	walk("", a, b)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Short renders a TOML value on one line, cut to n characters.
func Short(v any, n int) string {
	s := strings.ReplaceAll(fmt.Sprintf("%v", v), "\n", " ")
	switch x := v.(type) {
	case string:
		s = fmt.Sprintf("%q", x)
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0" // a TOML float, as written: 2.0, not 2
		}
	}
	if len(s) > n {
		s = s[:n-1] + "…"
	}
	return s
}

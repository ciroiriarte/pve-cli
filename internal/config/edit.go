package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// coerceScalar converts a flag-provided string to a typed value so typed config
// fields (bools like tls.verify, numbers like rate_limit.qps) round-trip
// correctly instead of being written as strings (which breaks typed loading).
func coerceScalar(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// GetValue reads a dotted key (e.g. "current_context" or
// "profiles.home.server") from the config file at path.
func GetValue(path, dotted string) (string, error) {
	m, err := loadMap(path)
	if err != nil {
		return "", err
	}
	keys := strings.Split(dotted, ".")
	var cur any = m
	for _, k := range keys {
		node, ok := cur.(map[string]any)
		if !ok {
			return "", fmt.Errorf("key %q not found", dotted)
		}
		cur, ok = node[k]
		if !ok {
			return "", fmt.Errorf("key %q not found", dotted)
		}
	}
	return fmt.Sprintf("%v", cur), nil
}

// SetValue sets a dotted key to a string value in the config file at path,
// creating intermediate maps as needed, and writes it back.
func SetValue(path, dotted, value string) error {
	m, err := loadMap(path)
	if err != nil {
		return err
	}
	keys := strings.Split(dotted, ".")
	cur := m
	for _, k := range keys[:len(keys)-1] {
		next, ok := cur[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[k] = next
		}
		cur = next
	}
	cur[keys[len(keys)-1]] = coerceScalar(value)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// loadMap loads the config file as a generic map (empty if the file is absent).
func loadMap(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	m := map[string]any{}
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return m, nil
}

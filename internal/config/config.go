// Package config reads and writes install settings: built-in defaults, then
// <BB_HOME>/config.yaml, then BB_* environment variables, merged with bkit
// settings.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/brian-nunez/bkit/settings"
	yamlfmt "github.com/brian-nunez/bkit/settings/drivers/yaml"
	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/errs"
	"github.com/brian-nunez/breadboard/internal/spec"
	"github.com/brian-nunez/breadboard/internal/state"
)

// FormatVersion is the config.yaml version this build writes.
const FormatVersion = 1

// EnvPrefix marks environment variables that override settings.
const EnvPrefix = "BB_"

// DefaultDomain is the default service domain. .internal is reserved for
// private use and never resolves publicly.
const DefaultDomain = "bb.internal"

// Settings are the effective install settings.
type Settings struct {
	Domain string
}

// Key describes one setting.
type Key struct {
	Name     string
	Help     string
	Default  string
	validate func(string) error
}

// privateSuffixes are special-use names that never resolve publicly.
var privateSuffixes = []string{".internal", ".test", ".localhost", ".home.arpa"}

var dnsName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Keys lists every setting.
var Keys = []Key{
	{
		Name:    "domain",
		Help:    "DNS domain for service names and certificates; changing it mints a new CA",
		Default: DefaultDomain,
		validate: func(v string) error {
			if !dnsName.MatchString(v) || len(v) > 200 {
				return errors.New("must be a lowercase DNS name with at least two labels, such as bb.internal")
			}

			if !slices.ContainsFunc(privateSuffixes, func(s string) bool { return strings.HasSuffix(v, s) }) {
				return fmt.Errorf("must end in a reserved private suffix (%s), so the CA can never sign for a real site", strings.Join(privateSuffixes, ", "))
			}

			return nil
		},
	},
}

func key(name string) (Key, bool) {
	i := slices.IndexFunc(Keys, func(k Key) bool { return k.Name == name })
	if i < 0 {
		return Key{}, false
	}

	return Keys[i], true
}

// EnvName is the environment variable that overrides a key.
func EnvName(name string) string { return EnvPrefix + strings.ToUpper(name) }

// Load returns the effective settings.
func Load(ctx context.Context, home state.Home) (Settings, error) {
	cfg, err := load(ctx, home)
	if err != nil {
		return Settings{}, err
	}

	s := Settings{Domain: cfg.GetString("domain", DefaultDomain)}
	if k, _ := key("domain"); k.validate(s.Domain) != nil {
		return Settings{}, spec.Invalid("domain", "%q %v (from %s)", s.Domain, k.validate(s.Domain), source(home, "domain"))
	}

	return s, nil
}

func load(ctx context.Context, home state.Home) (*settings.Config, error) {
	defaults := map[string]any{}
	for _, k := range Keys {
		defaults[k.Name] = k.Default
	}

	sources := []settings.Source{settings.Map(defaults)}

	if _, err := os.Stat(home.ConfigPath()); err == nil {
		if err := checkVersion(ctx, home); err != nil {
			return nil, err
		}

		sources = append(sources, settings.File(home.ConfigPath(), yamlfmt.Format))
	}

	sources = append(sources, settings.Env(settings.WithPrefix(EnvPrefix)))

	cfg, err := settings.Load(ctx, sources...)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	return cfg, nil
}

func checkVersion(ctx context.Context, home state.Home) error {
	cfg, err := settings.Load(ctx, settings.File(home.ConfigPath(), yamlfmt.Format))
	if err != nil {
		return fmt.Errorf("read %s: %w", home.ConfigPath(), err)
	}

	v, err := settings.Get[int](cfg, "version")
	if errors.Is(err, settings.ErrNotFound) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("read %s: version: %w", home.ConfigPath(), err)
	}

	if v > FormatVersion {
		return fmt.Errorf("%s has version %d: %w", home.ConfigPath(), v, errs.ErrNewerFormat)
	}

	return nil
}

// Value is a setting's effective value and where it came from.
type Value struct {
	Key    string
	Value  string
	Source string // "default", "config.yaml", or the env var name
	Help   string
}

// Get returns one setting.
func Get(ctx context.Context, home state.Home, name string) (Value, error) {
	k, ok := key(name)
	if !ok {
		return Value{}, unknown(name)
	}

	cfg, err := load(ctx, home)
	if err != nil {
		return Value{}, err
	}

	return Value{Key: name, Value: cfg.GetString(name, k.Default), Source: source(home, name), Help: k.Help}, nil
}

// All returns every setting.
func All(ctx context.Context, home state.Home) ([]Value, error) {
	out := make([]Value, 0, len(Keys))
	for _, k := range Keys {
		v, err := Get(ctx, home, k.Name)
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	return out, nil
}

func source(home state.Home, name string) string {
	if _, ok := os.LookupEnv(EnvName(name)); ok {
		return EnvName(name)
	}

	data, err := os.ReadFile(home.ConfigPath())
	if err == nil {
		var m map[string]any
		if yaml.Unmarshal(data, &m) == nil {
			if _, ok := m[name]; ok {
				return "config.yaml"
			}
		}
	}

	return "default"
}

// Set validates and writes one setting to config.yaml, keeping the file's
// comments. It returns the new value as stored; Source names an environment
// variable when that variable still overrides the file.
func Set(ctx context.Context, home state.Home, name, value string) (Value, error) {
	k, ok := key(name)
	if !ok {
		return Value{}, unknown(name)
	}

	if err := k.validate(value); err != nil {
		return Value{}, spec.Invalid(name, "%v", err)
	}

	if _, err := home.Ensure(); err != nil {
		return Value{}, err
	}

	if _, err := os.Stat(home.ConfigPath()); err == nil {
		if err := checkVersion(ctx, home); err != nil {
			return Value{}, err
		}
	}

	doc, err := readDoc(home.ConfigPath())
	if err != nil {
		return Value{}, err
	}

	setKey(doc.Content[0], "version", fmt.Sprint(FormatVersion), "!!int", "")
	setKey(doc.Content[0], name, value, "!!str", k.Help)

	out, err := yaml.Marshal(doc)
	if err != nil {
		return Value{}, err
	}

	if err := state.WriteFile(home.ConfigPath(), out, 0o600); err != nil {
		return Value{}, err
	}

	return Get(ctx, home, name)
}

func readDoc(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	var doc yaml.Node
	if len(data) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
	}

	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}

	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("read %s: the top level must be a mapping", path)
	}

	return &doc, nil
}

func setKey(m *yaml.Node, name, value, tag, comment string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			v := m.Content[i+1]
			v.Kind, v.Tag, v.Value, v.Style = yaml.ScalarNode, tag, value, 0
			v.Content = nil

			return
		}
	}

	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
	if comment != "" {
		k.HeadComment = comment
	}

	m.Content = append(m.Content, k, &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
}

func unknown(name string) error {
	names := make([]string, len(Keys))
	for i, k := range Keys {
		names[i] = k.Name
	}

	return fmt.Errorf("setting %q (known: %s): %w", name, strings.Join(names, ", "), errs.ErrNotFound)
}

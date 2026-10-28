package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// config is the launcher's state in %APPDATA%\WatchZit Launcher\launcher.json:
// paired servers, programs the user allowed, nonces of used links and
// paths of tools in unusual places.
type config struct {
	Servers []server          `json:"servers"`
	Allowed []string          `json:"allowed,omitempty"`
	Paths   map[string]string `json:"paths,omitempty"`
	Used    []usedNonce       `json:"used,omitempty"`
	path    string
}

type server struct {
	ID    string    `json:"id"`
	Key   string    `json:"key"` // base64url public key
	Name  string    `json:"name"`
	Added time.Time `json:"added"`
}

type usedNonce struct {
	Nonce   string `json:"n"`
	Expires int64  `json:"e"`
}

func (s server) pub() ed25519.PublicKey {
	k, err := base64.RawURLEncoding.DecodeString(s.Key)
	if err != nil || len(k) != ed25519.PublicKeySize {
		return nil
	}
	return k
}

func loadConfig(dir string) (*config, error) {
	c := &config{path: filepath.Join(dir, "launcher.json"), Paths: map[string]string{}}
	b, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, errors.New("launcher.json cannot be read: " + err.Error())
	}
	if c.Paths == nil {
		c.Paths = map[string]string{}
	}
	return c, nil
}

func (c *config) save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *config) server(id string) *server {
	for i := range c.Servers {
		if c.Servers[i].ID == id {
			return &c.Servers[i]
		}
	}
	return nil
}

// useNonce records a link's nonce; false when it was used before.
func (c *config) useNonce(n string, exp int64, now time.Time) bool {
	keep := c.Used[:0]
	seen := false
	for _, u := range c.Used {
		if u.Expires+600 < now.Unix() {
			continue // long expired: the link is refused anyway
		}
		seen = seen || u.Nonce == n
		keep = append(keep, u)
	}
	c.Used = keep
	if seen {
		return false
	}
	c.Used = append(c.Used, usedNonce{Nonce: n, Expires: exp})
	return true
}

func (c *config) allowed(program string) bool {
	return slices.ContainsFunc(c.Allowed, func(a string) bool { return strings.EqualFold(a, program) })
}

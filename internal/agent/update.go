package agent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

var updating atomic.Bool

// Install starts the downloaded installer (tests replace it): msiexec on
// Windows, the new program in place of the old one on Linux.
var Install = installUpdate

// selfUpdate downloads the agent the server names over the pinned channel,
// checks it against the size and SHA-256 the server sent, and installs it.
// Only newer versions; one update at a time.
func (a *Agent) selfUpdate(ctx context.Context, hc *http.Client, u proto.AgentUpdate) error {
	want, err := hex.DecodeString(u.SHA256)
	if !proto.ValidVersion(u.Version) || err != nil || len(want) != sha256.Size || u.Size <= 0 || u.Size > proto.MaxPackageSize || u.Name == "" || u.Name != filepath.Base(u.Name) {
		return errors.New("bad update request")
	}
	if proto.CompareVersions(u.Version, Version) <= 0 {
		return nil // already this version or newer
	}
	if !updating.CompareAndSwap(false, true) {
		return nil
	}
	ok := false
	defer func() {
		if !ok {
			updating.Store(false)
		}
	}()
	dir := filepath.Join(a.Dir, "updates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, u.Name)
	dctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, a.Cfg.ServerURL+"/agent/download/"+url.PathEscape(u.Name), nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", resp.Status)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700) // #nosec G304 G302 -- our own updates folder, a plain file name; executable: the Linux agent runs it
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, u.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != u.Size || subtle.ConstantTimeCompare(h.Sum(nil), want) != 1 {
		_ = os.Remove(path)
		return errors.New("the downloaded agent does not match the server's checksum")
	}
	a.Log.Info("installing agent update", "version", u.Version, "file", path)
	if err := Install(path, u.Version, filepath.Join(dir, "update-"+u.Version+".log")); err != nil {
		return fmt.Errorf("cannot install the update: %w", err)
	}
	ok = true
	// the installer replaces this agent; if that does not happen, allow
	// another attempt later
	time.AfterFunc(15*time.Minute, func() { updating.Store(false) })
	return nil
}

// cleanUpdates removes installers an earlier update left behind (the MSI
// stays while msiexec runs); installer logs stay for a month.
func cleanUpdates(dir string, now time.Time) {
	list, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range list {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		keep := time.Hour
		if filepath.Ext(e.Name()) == ".log" {
			keep = 30 * 24 * time.Hour
		}
		if now.Sub(info.ModTime()) > keep {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

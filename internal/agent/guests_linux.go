//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

const dockerSocket = "/var/run/docker.sock"

// guests lists the Docker containers when the Docker socket is there.
func guests(ctx context.Context) *proto.Guests {
	hc := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", dockerSocket)
		}}}
	defer hc.CloseIdleConnections()
	get := func(path string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, io.ErrUnexpectedEOF
		}
		return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	}
	data, err := get("/containers/json?all=1")
	if err != nil {
		return nil
	}
	var ver struct {
		Version string `json:"Version"`
	}
	if v, err := get("/version"); err == nil {
		_ = json.Unmarshal(v, &ver)
	}
	g, err := parseDocker(data, ver.Version)
	if err != nil {
		return nil
	}
	return g
}

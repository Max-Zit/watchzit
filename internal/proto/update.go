package proto

import (
	"strconv"
	"strings"
)

// Agent self-update. On the authenticated channel (TLS pinned to the
// server's certificate) the server names the version, the installer and
// its SHA-256; the agent downloads the installer from the agent endpoint
// over the same pinned TLS, checks size and hash and installs it.

// AgentUpdate names the installer to update to.
type AgentUpdate struct {
	Version string `json:"version"`
	Name    string `json:"name"` // file under /agent/download/
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// MaxPackageSize limits the installer an agent downloads.
const MaxPackageSize = 200 << 20

// CompareVersions compares dotted versions such as 1.2.10 and 1.2.9
// numerically (-1, 0, 1). A suffix after the numbers ("-dev") sorts before
// the plain version.
func CompareVersions(a, b string) int {
	na, sa := splitVersion(a)
	nb, sb := splitVersion(b)
	for i := 0; i < len(na) || i < len(nb); i++ {
		var x, y int
		if i < len(na) {
			x = na[i]
		}
		if i < len(nb) {
			y = nb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case sa == sb:
		return 0
	case sa == "":
		return 1
	case sb == "":
		return -1
	case sa < sb:
		return -1
	}
	return 1
}

func splitVersion(v string) ([]int, string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	suffix := ""
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v, suffix = v[:i], v[i:]
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, suffix + "?" + p
		}
		out = append(out, n)
	}
	return out, suffix
}

// ValidVersion reports whether v looks like 1.2.3.
func ValidVersion(v string) bool {
	n, s := splitVersion(v)
	return len(n) == 3 && s == ""
}

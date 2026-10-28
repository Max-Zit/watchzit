// Command watchzit-launcher opens devices from the WatchZit console with
// programs on this computer (PuTTY or OpenSSH, Remote Desktop, WinBox, VNC,
// programs the administrator adds). Windows passes it watchzit:// links; it
// trusts only servers the user paired and checks every link's signature,
// age and fields before it starts anything (see internal/launch).
package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/launch"
)

var version = "dev"

// ui asks and tells the user (message boxes on Windows).
type ui interface {
	ask(title, text string) bool
	info(title, text string)
	fail(title, text string)
}

// launcher handles one link.
type launcher struct {
	cfg  *config
	ui   ui
	now  func() time.Time
	find func(tool string) string                  // program of a built-in tool, "" if missing
	temp string                                    // folder for .rdp files
	run  func(program string, args []string) error // starts a program
	log  func(format string, args ...any)
}

func (l *launcher) handle(raw string) error {
	link, err := launch.Parse(raw)
	if err != nil {
		return err
	}
	switch link.Action {
	case "pair":
		return l.pair(link)
	case "open":
		return l.open(link)
	}
	return errors.New("unknown WatchZit link")
}

func (l *launcher) pair(link *launch.Link) error {
	id := launch.KeyID(link.Key)
	if s := l.cfg.server(id); s != nil {
		l.ui.info("WatchZit Launcher", fmt.Sprintf("This computer is already paired with %s.", s.Name))
		return nil
	}
	text := fmt.Sprintf("Pair this computer with the WatchZit server %s?\n\nFingerprint:\n%s\n\nCompare it with the Launcher page of the console. "+
		"Only pair servers you trust: they can open devices with programs on this computer.", link.Name, launch.Fingerprint(link.Key))
	if !l.ui.ask("WatchZit Launcher", text) {
		l.log("pairing with %s (%s) refused", link.Name, id)
		return nil
	}
	l.cfg.Servers = append(l.cfg.Servers, server{ID: id, Key: base64.RawURLEncoding.EncodeToString(link.Key), Name: link.Name, Added: l.now().UTC()})
	if err := l.cfg.save(); err != nil {
		return err
	}
	l.log("paired with %s (%s)", link.Name, id)
	l.ui.info("WatchZit Launcher", fmt.Sprintf("Paired with %s. Devices now open from its console.", link.Name))
	return nil
}

func (l *launcher) open(link *launch.Link) error {
	srv := l.cfg.server(link.ServerID())
	if srv == nil || srv.pub() == nil {
		return errors.New("this link comes from a server this computer is not paired with. Open the Launcher page of the console and click Pair this computer")
	}
	p, err := link.Verify(srv.pub(), l.now())
	if err != nil {
		return err
	}
	if !l.cfg.useNonce(p.Nonce, p.Expires, l.now()) {
		return errors.New("this link was already used; click Open with in the console again")
	}
	if err := l.cfg.save(); err != nil {
		return err
	}
	prog, args, err := l.command(p)
	if err != nil {
		return err
	}
	if p.Tool == launch.ToolCustom && !l.cfg.allowed(prog) {
		text := fmt.Sprintf("The WatchZit server %s wants to start this program:\n\n%s\n\nfor %s (%s). Allow it? WatchZit asks only once for each program.", srv.Name, prog, p.Device, p.Host)
		if !l.ui.ask("WatchZit Launcher", text) {
			l.log("%s refused for %s", prog, srv.Name)
			return nil
		}
		l.cfg.Allowed = append(l.cfg.Allowed, prog)
		if err := l.cfg.save(); err != nil {
			return err
		}
	}
	l.log("%s -> %s %s:%d: %s %s", srv.Name, p.Name, p.Host, p.Port, prog, strings.Join(args, " "))
	return l.run(prog, args)
}

// command builds the program and arguments of a link (no shell is used).
func (l *launcher) command(p *launch.Payload) (string, []string, error) {
	port := strconv.Itoa(p.Port)
	missing := func(what, how string) error {
		return fmt.Errorf("%s was not found on this computer. %s", what, how)
	}
	switch p.Tool {
	case launch.ToolSSH:
		if putty := l.find("putty"); putty != "" {
			args := []string{"-ssh", "-P", port}
			if p.User != "" {
				args = append(args, "-l", p.User)
			}
			return putty, append(args, p.Host), nil
		}
		ssh := l.find("ssh")
		if ssh == "" {
			return "", nil, missing("An SSH client", "Install PuTTY, or the OpenSSH client (Windows Settings → Optional features).")
		}
		args := []string{"-p", port}
		if p.User != "" {
			args = append(args, "-l", p.User)
		}
		return ssh, append(args, p.Host), nil
	case launch.ToolTelnet:
		putty := l.find("putty")
		if putty == "" {
			return "", nil, missing("PuTTY", "Install it for Telnet.")
		}
		return putty, []string{"-telnet", "-P", port, p.Host}, nil
	case launch.ToolRDP:
		mstsc := l.find("mstsc")
		if mstsc == "" {
			return "", nil, missing("Remote Desktop (mstsc.exe)", "")
		}
		f, err := l.rdpFile(p)
		if err != nil {
			return "", nil, err
		}
		return mstsc, []string{f}, nil
	case launch.ToolWinBox:
		w := l.find("winbox")
		if w == "" {
			return "", nil, missing("WinBox", `Put winbox64.exe on the Desktop or in Downloads, or set its path in %APPDATA%\WatchZit Launcher\launcher.json.`)
		}
		addr := p.Host
		if p.Port != 8291 {
			addr += ":" + port
		}
		args := []string{addr}
		if p.User != "" {
			args = append(args, p.User, "")
		}
		return w, args, nil
	case launch.ToolVNC:
		v := l.find("vnc")
		if v == "" {
			return "", nil, missing("A VNC viewer", "Install TightVNC, TigerVNC or RealVNC viewer.")
		}
		return v, []string{p.Host + "::" + port}, nil
	case launch.ToolCustom:
		prog := l.find("custom:" + p.Program)
		if prog == "" {
			return "", nil, fmt.Errorf("the program %s of the tool %s is not on this computer", p.Program, p.Name)
		}
		return prog, p.Expand(), nil
	}
	return "", nil, errors.New("unknown tool")
}

// rdpFile writes a Remote Desktop file for the link (mstsc asks for the
// password; files older than a day are removed).
func (l *launcher) rdpFile(p *launch.Payload) (string, error) {
	if err := os.MkdirAll(l.temp, 0o700); err != nil {
		return "", err
	}
	if old, err := os.ReadDir(l.temp); err == nil {
		for _, e := range old {
			if i, err := e.Info(); err == nil && l.now().Sub(i.ModTime()) > 24*time.Hour {
				_ = os.Remove(filepath.Join(l.temp, e.Name()))
			}
		}
	}
	var b strings.Builder
	b.WriteString("full address:s:" + p.Host + ":" + strconv.Itoa(p.Port) + "\r\n")
	if p.User != "" {
		b.WriteString("username:s:" + p.User + "\r\n")
	}
	b.WriteString("prompt for credentials:i:1\r\nsmart sizing:i:1\r\n")
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		return '-'
	}, p.Host)
	f := filepath.Join(l.temp, name+"-"+p.Nonce[:8]+".rdp")
	return f, os.WriteFile(f, []byte(b.String()), 0o600) // #nosec G703 -- own temp folder; the name is the checked host with unsafe characters replaced
}

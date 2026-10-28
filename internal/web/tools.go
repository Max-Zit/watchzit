package web

import (
	"context"
	"crypto/ed25519"
	"html/template"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/launch"
	"github.com/max-zit/watchzit/internal/store"
)

// Opening devices with local programs: the console signs a watchzit:// link
// for the WatchZit Launcher (see internal/launch). Remote Desktop also works
// without it, as a downloaded .rdp file.

// deviceTool is a tool offered on a device, with the port it uses there.
type deviceTool struct {
	*store.Tool
	DevPort int
}

// toolsFor lists the enabled tools for a device of the given kind.
func (s *Server) toolsFor(ctx context.Context, kind string, access store.DeviceAccess) []deviceTool {
	ts, err := s.store.Tools(ctx)
	if err != nil {
		s.log.Error("cannot load tools", "err", err)
		return nil
	}
	var out []deviceTool
	for _, t := range ts {
		if t.For(kind) {
			out = append(out, deviceTool{Tool: t, DevPort: access.PortFor(t)})
		}
	}
	return out
}

// launcherPub is the public key launchers pair with.
func (s *Server) launcherPub() ed25519.PublicKey {
	if s.LauncherKey == nil {
		return nil
	}
	return s.LauncherKey.Public().(ed25519.PublicKey)
}

// handleOpen answers a click on "Open with …": a redirect to a signed
// watchzit:// link. Only clicks from this console's own pages are served
// (another site must not make the browser start a program).
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if sf := r.Header.Get("Sec-Fetch-Site"); sf != "" && sf != "same-origin" && sf != "none" {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	dev := s.loadDevice(w, r)
	if dev == nil {
		return
	}
	tid, ok := pathID(r, "tool")
	if !ok {
		s.notFound(w, r)
		return
	}
	t, err := s.store.ToolByID(r.Context(), tid)
	if err != nil || !t.Enabled {
		s.notFound(w, r)
		return
	}
	access, err := s.store.DeviceAccess(r.Context(), dev.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	back := "/devices/" + strconv.FormatInt(dev.ID, 10)
	if s.LauncherKey == nil {
		s.redirectErr(w, r, back, "The launcher key is missing on the server.")
		return
	}
	p := launch.Payload{Tool: t.Builtin, Name: t.Name, Host: dev.Address, Port: access.PortFor(t), User: access.User, Device: dev.Name}
	if t.Builtin == "" {
		p.Tool, p.Program, p.Args = launch.ToolCustom, t.Program, t.Args
	}
	link, err := launch.OpenURL(s.LauncherKey, p, s.now())
	if err != nil {
		s.redirectErr(w, r, back, err.Error())
		return
	}
	s.audit(r, "device.open", "device:"+strconv.FormatInt(dev.ID, 10), "%s opened with %s (%s:%d, user %q)", dev.Name, t.Name, dev.Address, p.Port, p.User)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, link, http.StatusFound)
}

var fileNameRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// handleRDP sends a Remote Desktop file for the device (works without the
// launcher; mstsc asks for the password).
func (s *Server) handleRDP(w http.ResponseWriter, r *http.Request) {
	dev := s.loadDevice(w, r)
	if dev == nil {
		return
	}
	access, err := s.store.DeviceAccess(r.Context(), dev.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	port := 3389
	if ts, err := s.store.Tools(r.Context()); err == nil {
		for _, t := range ts {
			if t.Builtin == launch.ToolRDP {
				port = access.PortFor(t)
			}
		}
	}
	if !launch.ValidHost(dev.Address) {
		s.redirectErr(w, r, "/devices/"+strconv.FormatInt(dev.ID, 10), "the device address cannot be used in a link")
		return
	}
	var b strings.Builder
	b.WriteString("full address:s:" + dev.Address + ":" + strconv.Itoa(port) + "\r\n")
	if launch.ValidUser(access.User) && access.User != "" {
		b.WriteString("username:s:" + access.User + "\r\n")
	}
	b.WriteString("prompt for credentials:i:1\r\nscreen mode id:i:1\r\ndesktopwidth:i:1600\r\ndesktopheight:i:900\r\nsmart sizing:i:1\r\n")
	name := strings.Trim(fileNameRE.ReplaceAllString(dev.Name, "-"), "-")
	if name == "" {
		name = "device"
	}
	s.audit(r, "device.rdp", "device:"+strconv.FormatInt(dev.ID, 10), "Remote Desktop file for %s downloaded", dev.Name)
	w.Header().Set("Content-Type", "application/x-rdp")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.rdp"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b.String()))
}

// ---- Launcher page (every user pairs their own computer)

type launcherData struct {
	PairURL     template.URL // built by the server: a watchzit:// link html/template would otherwise blank
	Fingerprint string
	Download    bool
	Tools       []*store.Tool
}

// serverName is how a launcher names this server: the console host.
func serverName(r *http.Request) string {
	h := r.Host
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.HasSuffix(h, "]") {
		h = h[:i]
	}
	h = strings.Trim(h, "[]")
	if h == "" || len(h) > 100 {
		return "WatchZit"
	}
	return h
}

func (s *Server) handleLauncher(w http.ResponseWriter, r *http.Request) {
	d := &launcherData{}
	if pub := s.launcherPub(); pub != nil {
		d.PairURL = template.URL(launch.PairURL(pub, serverName(r))) // #nosec G203 -- own link, fields are encoded
		d.Fingerprint = launch.Fingerprint(pub)
	}
	_, d.Download = s.agentDownloads()["launcher"]
	d.Tools, _ = s.store.Tools(r.Context())
	s.render(w, r, http.StatusOK, "launcher", &Page{Title: "Launcher", Nav: "launcher", Data: d})
}

// ---- Settings → Tools (administrators)

type toolsData struct {
	Tools []*store.Tool
	Edit  *store.Tool
	In    store.ToolInput
	Args  string
	Kinds []string
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	d := &toolsData{In: store.ToolInput{Enabled: true, Port: 22}}
	if id, ok := pathID(r, "id"); ok {
		t, err := s.store.ToolByID(r.Context(), id)
		if err != nil {
			s.notFound(w, r)
			return
		}
		d.Edit = t
		d.In = store.ToolInput{Name: t.Name, Program: t.Program, Args: t.Args, Port: t.Port, Kinds: t.Kinds, Enabled: t.Enabled}
		d.Args = launch.JoinArgs(t.Args)
	}
	s.showTools(w, r, http.StatusOK, d, "")
}

func (s *Server) showTools(w http.ResponseWriter, r *http.Request, status int, d *toolsData, errMsg string) {
	var err error
	if d.Tools, err = s.store.Tools(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Kinds = store.DeviceKinds
	p := &Page{Title: "Tools", Nav: "settings", Data: d}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "tools", p)
}

func toolInput(r *http.Request) (store.ToolInput, string, error) {
	_ = r.ParseForm()
	in := store.ToolInput{Name: r.FormValue("name"), Program: strings.TrimSpace(r.FormValue("program")), Port: formInt(r, "port"),
		Enabled: r.FormValue("enabled") == "1"}
	for _, k := range r.Form["kind"] {
		if !slices.Contains(in.Kinds, k) {
			in.Kinds = append(in.Kinds, k)
		}
	}
	line := strings.TrimSpace(r.FormValue("args"))
	args, err := launch.SplitArgs(line)
	in.Args = args
	return in, line, err
}

func (s *Server) handleToolCreate(w http.ResponseWriter, r *http.Request) {
	in, line, err := toolInput(r)
	var id int64
	if err == nil {
		id, err = s.store.CreateTool(r.Context(), in)
	}
	if err != nil {
		s.showTools(w, r, http.StatusBadRequest, &toolsData{In: in, Args: line}, storeErr(err))
		return
	}
	s.audit(r, "tool.create", "tool:"+strconv.FormatInt(id, 10), "tool %q added: %s %s", in.Name, in.Program, line)
	s.redirect(w, r, "/settings/tools", "Tool added.")
}

func (s *Server) handleToolUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	t, err := s.store.ToolByID(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	in, line, err := toolInput(r)
	if err == nil {
		err = s.store.UpdateTool(r.Context(), id, in)
	}
	if err != nil {
		s.showTools(w, r, http.StatusBadRequest, &toolsData{Edit: t, In: in, Args: line}, storeErr(err))
		return
	}
	s.audit(r, "tool.update", "tool:"+strconv.FormatInt(id, 10), "tool %q changed: enabled %v, port %d, kinds %v, %s %s", in.Name, in.Enabled, in.Port, in.Kinds, in.Program, line)
	s.redirect(w, r, "/settings/tools", "Saved.")
}

func (s *Server) handleToolDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	t, err := s.store.ToolByID(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := s.store.DeleteTool(r.Context(), id); err != nil {
		s.redirectErr(w, r, "/settings/tools", err.Error())
		return
	}
	s.audit(r, "tool.delete", "tool:"+strconv.FormatInt(id, 10), "tool %q deleted", t.Name)
	s.redirect(w, r, "/settings/tools", "Tool deleted.")
}

// accessFromForm reads the device form's access fields.
func accessFromForm(r *http.Request) store.DeviceAccess {
	_ = r.ParseForm()
	a := store.DeviceAccess{User: strings.TrimSpace(r.FormValue("access_user")), Ports: map[int64]int{}}
	for k, v := range r.Form {
		if id, ok := strings.CutPrefix(k, "port_"); ok && len(v) > 0 && strings.TrimSpace(v[0]) != "" {
			tid, err1 := strconv.ParseInt(id, 10, 64)
			p, err2 := strconv.Atoi(strings.TrimSpace(v[0]))
			if err1 == nil && err2 == nil {
				a.Ports[tid] = p
			}
		}
	}
	return a
}

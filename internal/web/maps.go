package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/store"
)

// ---- map list

type mapsData struct {
	Maps []*store.Map
	Name string
}

func (s *Server) handleMaps(w http.ResponseWriter, r *http.Request) {
	s.showMaps(w, r, http.StatusOK, "", "")
}

func (s *Server) showMaps(w http.ResponseWriter, r *http.Request, status int, name, errMsg string) {
	ms, err := s.store.Maps(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p := &Page{Title: "Maps", Nav: "maps", Data: &mapsData{Maps: ms, Name: name}}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "maps", p)
}

func (s *Server) handleMapCreate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	id, err := s.store.CreateMap(r.Context(), name, r.FormValue("notes"))
	if err != nil {
		s.showMaps(w, r, http.StatusBadRequest, name, storeErr(err))
		return
	}
	s.audit(r, "map.create", "map:"+strconv.FormatInt(id, 10), "map %q added", strings.TrimSpace(name))
	http.Redirect(w, r, "/maps/"+strconv.FormatInt(id, 10)+"?edit=1", http.StatusSeeOther)
}

func (s *Server) loadMap(w http.ResponseWriter, r *http.Request) *store.Map {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return nil
	}
	m, err := s.store.MapByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	return m
}

func (s *Server) handleMapUpdate(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	name := r.FormValue("name")
	if err := s.store.UpdateMap(r.Context(), m.ID, name, r.FormValue("notes")); err != nil {
		s.redirectErr(w, r, "/maps", storeErr(err))
		return
	}
	s.audit(r, "map.update", "map:"+strconv.FormatInt(m.ID, 10), "map %q renamed to %q", m.Name, strings.TrimSpace(name))
	s.redirect(w, r, "/maps", "Saved.")
}

func (s *Server) handleMapDelete(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	if err := s.store.DeleteMap(r.Context(), m.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "map.delete", "map:"+strconv.FormatInt(m.ID, 10), "map %q deleted (devices stay)", m.Name)
	s.redirect(w, r, "/maps", "Map deleted.")
}

// ---- the map page; map.js draws it from the JSON below

type mapPageData struct {
	M    *store.Map
	Edit bool
}

func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	edit := r.FormValue("edit") == "1" && userFrom(r).CanWrite()
	s.render(w, r, http.StatusOK, "map", &Page{Title: m.Name, Nav: "maps", Data: &mapPageData{M: m, Edit: edit}})
}

// mapText are the words map.js shows, translated on the server.
var mapText = []string{"Edit", "Done", "Zoom in", "Zoom out", "Fit to screen", "Add device", "Add submap", "Add text", "Connect", "Delete",
	"Saved", "Saving…", "Could not save.", "Unsaved changes", "Uploading…", "Discard the changes made since the last save?", "Search", "All devices are on this map.", "Click the first device, then the second.",
	"Path", "Layers", "Show only how two devices are connected", "Click the first device, then the second; the map then shows only how they are connected.", "These two are not connected on this map.", "cable", "cables", "Click Path or press Esc to show the whole map.", "Show on the map", "Devices", "Racks", "Items", "Submaps", "Cables", "Port names", "Traffic", "Names and addresses", "Background", "Show all", "VLANs", "All VLANs", "tagged", "untagged", "detected", "VLAN mode", "Access", "Trunk", "Hybrid", "native", "One end is an access port, the other a trunk:", "The native VLAN differs:", "Access VLAN", "Native VLAN (untagged)", "Allowed VLANs (tagged)", "Automatic uses the VLANs the ports report.", "Tagged or untagged", "Tagged and untagged", "Only tagged", "Only untagged", "VLAN labels", "VXLAN tunnels", "Graphs", "Metrics", "SSH, Remote Desktop and the other tools open through the WatchZit Launcher.", "Set it up", "Search, e.g. DL380 or CRS326", "Generic pictures", "Manufacturer",
	"Open device", "Open web interface", "Open map", "Port", "Not set", "Kind", "Wired", "Wireless", "VPN", "Label", "In", "Out", "Speed",
	"Checks", "down", "Up", "Down", "Warning", "Unknown", "Paused", "No link", "Disabled", "Text", "Ports", "Link", "No other maps yet.",
	"Drag devices to place them. Use Connect to draw a link and choose the ports.", "Address", "Status", "Suggested links", "Add", "Neighbours found over LLDP and MikroTik discovery.",
	"No suggestions.", "Close", "Vendor",
	"Item", "Picture", "Change picture", "Symbol (no picture)", "Upload picture", "Size", "Rack", "Not in a rack", "Mounted at unit", "Devices in rack", "Rack name", "Units",
	"Drag devices into the rack; they snap to the units.", "Port on picture", "Interface", "Cable", "Route", "Straight", "90° bends", "45° bends", "Curved",
	"Drag the + points to bend the cable; drag the round points to move a bend; double-click a point to remove it.", "Background", "Remove background",
	"Without a picture the map shows the dotted grid.", "Opacity", "Move background (drag it)", "The picture could not be uploaded.",
	"Power", "Logical", "Uploaded", "All types", "Switch", "Router", "Firewall", "Server", "Storage / NAS", "Access point", "UPS", "Camera", "Computer", "Printer", "Other", "All manufacturers", "No pictures match.", "Type", "Picture library", "Add rack", "Port", "Automatic", "Hypervisor", "Hypervisors", "Physical server", "Virtual machines", "Virtual machine", "Runs on hypervisor", "None", "Add hypervisor", "connected to", "runs on", "Choose the server", "Choose the physical server the hypervisor runs on.", "Drag virtual machines here.", "No virtual machines yet.", "Drag virtual machines and devices into the box; drag them within it to change the order. Add a VM as a device of the console to see its state, or as an item with a picture of its system.", "Monitored device", "None (picture only)"}

type mapJSON struct {
	ID      int64                 `json:"id"`
	Name    string                `json:"name"`
	CanEdit bool                  `json:"can_edit"`
	Layout  *store.MapLayout      `json:"layout"`
	Devices []mapDevice           `json:"devices,omitempty"` // for "Add device"
	Maps    []mapRef              `json:"maps"`              // for submaps and names
	Text    map[string]string     `json:"text"`
	Assets  map[int64]store.Asset `json:"assets"`
}

type mapDevice struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Kind    string `json:"kind"`
	Descr   string `json:"descr,omitempty"` // SNMP system description
	Vendor  string `json:"vendor,omitempty"`
}

type mapRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleMapLayout(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	u := userFrom(r)
	s.writeMapJSON(w, r, m, u.CanWrite(), s.lang(r, u))
}

// writeMapJSON sends a map's layout with what map.js needs to draw it.
func (s *Server) writeMapJSON(w http.ResponseWriter, r *http.Request, m *store.Map, canEdit bool, lang string) {
	ctx := r.Context()
	l, err := s.store.MapLayout(ctx, m.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out := mapJSON{ID: m.ID, Name: m.Name, CanEdit: canEdit, Layout: l, Maps: []mapRef{}, Text: map[string]string{}, Assets: map[int64]store.Asset{}}
	for _, t := range mapText {
		out.Text[t] = translate(lang, t)
	}
	for k, n := range kindNames {
		out.Text["kind:"+k] = translate(lang, n)
	}
	for k, n := range linkKindNames {
		out.Text["cable:"+k] = translate(lang, n)
	}
	ms, err := s.store.Maps(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, x := range ms {
		out.Maps = append(out.Maps, mapRef{x.ID, x.Name})
	}
	// sizes of the uploaded pictures on this map
	for _, kind := range []string{"icon", "background"} {
		as, err := s.store.Assets(ctx, kind)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		for _, a := range as {
			out.Assets[a.ID] = a
		}
	}
	if !canEdit {
		used := map[int64]bool{l.Background.Asset: true}
		for _, n := range l.Nodes {
			if id, ok := strings.CutPrefix(n.Icon, "asset:"); ok {
				x, _ := strconv.ParseInt(id, 10, 64)
				used[x] = true
			}
		}
		for id, a := range out.Assets {
			if !used[id] && a.Meta.Replaces == "" { // a changed built-in picture may be on the map
				delete(out.Assets, id)
			}
		}
	}
	if canEdit {
		ds, err := s.store.Devices(ctx, store.DeviceFilter{})
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		descrs, err := s.store.DeviceDescrs(ctx)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		for _, d := range ds {
			out.Devices = append(out.Devices, mapDevice{d.ID, d.Name, d.Address, d.Kind, descrs[d.ID][0], descrs[d.ID][1]})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// linkKindNames name the cable kinds.
var linkKindNames = map[string]string{"ethernet": "Ethernet (copper)", "fiber": "Fibre, multimode", "fiber-sm": "Fibre, single mode", "dac": "DAC cable",
	"wireless": "Wireless", "vpn": "VPN tunnel", "vxlan": "VXLAN tunnel", "wan": "WAN / internet line", "serial": "Serial / console", "power": "Power"}

func (s *Server) handleMapLive(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	s.writeMapLive(w, r, m, true)
}

// writeMapLive sends the live state of a map; withTools adds the tools that
// open each device (the console, not kiosk displays).
func (s *Server) writeMapLive(w http.ResponseWriter, r *http.Request, m *store.Map, withTools bool) {
	l, err := s.store.MapLayout(r.Context(), m.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	live, err := s.store.MapLiveData(r.Context(), l)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if withTools {
		if ts, err := s.store.Tools(r.Context()); err == nil {
			for _, d := range live.Devices {
				for _, t := range ts {
					if t.For(d.Kind) {
						k := t.Builtin
						if k == "" {
							k = "custom"
						}
						d.Tools = append(d.Tools, store.LiveTool{ID: t.ID, Name: t.Name, Kind: k})
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, live)
}

// handleMapSave stores the editor's layout (JSON body).
func (s *Server) handleMapSave(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "JSON expected"})
		return
	}
	var in store.MapLayout
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid layout"})
		return
	}
	ctx := r.Context()
	before, err := s.store.MapLayout(ctx, m.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out, err := s.store.SaveMapLayout(ctx, m.ID, &in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": translate(s.lang(r, userFrom(r)), storeErr(err))})
		return
	}
	// moves are not audited; added or removed nodes and links are
	if structure(before) != structure(out) {
		s.audit(r, "map.save", "map:"+strconv.FormatInt(m.ID, 10), "map %q saved: %d nodes, %d links (before %d, %d)", m.Name, len(out.Nodes), len(out.Links), len(before.Nodes), len(before.Links))
	}
	writeJSON(w, http.StatusOK, out)
}

// structure sums up a layout without positions.
func structure(l *store.MapLayout) string {
	var b strings.Builder
	for _, n := range l.Nodes {
		b.WriteString(n.Kind + strconv.FormatInt(n.DeviceID, 10) + "/" + strconv.FormatInt(n.SubmapID, 10) + n.Label + ";")
	}
	for _, k := range l.Links {
		b.WriteString(strconv.FormatInt(k.A, 10) + "-" + strconv.FormatInt(k.B, 10) + ":" + strconv.Itoa(k.AIf) + "/" + strconv.Itoa(k.BIf) + k.Kind + k.Label + ";")
		for _, v := range k.VLANs {
			b.WriteString("v" + strconv.Itoa(v.ID) + strconv.FormatBool(v.Tagged))
		}
	}
	return b.String()
}

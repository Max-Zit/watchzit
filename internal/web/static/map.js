// WatchZit network map: a drawing of the network that is live monitoring.
// Devices are placed by drag and drop and linked port to port; colours
// show their state and link widths the traffic. Own SVG code (the CSP
// allows only scripts of this console): wheel and pinch zoom around the
// pointer, drag to pan, semantic zoom (details fade out when far away).
(function () {
  "use strict";
  var root = document.getElementById("mapview");
  if (!root) return;
  var mapID = root.getAttribute("data-map");
  var svg = root.querySelector("svg.mapsvg");
  var vp = svg.querySelector("g.viewport");
  var gLinks = svg.querySelector("g.links");
  var gNodes = svg.querySelector("g.nodes");
  var panel = root.querySelector("[data-panel]");
  var hint = root.querySelector("[data-hint]");
  var saveState = root.querySelector("[data-save]");
  var NS = "http://www.w3.org/2000/svg";

  var meta = null;          // map name, devices, maps, words
  var layout = { nodes: [], links: [] };
  var live = { devices: {}, ports: {}, maps: {} };
  var view = { x: 0, y: 0, k: 1 };
  var editing = root.getAttribute("data-edit") === "1";
  var selected = null;      // {type: "node"|"link", id}
  var connectFrom = null;   // node id while drawing a link; 0 = waiting for the first
  var connecting = false;
  var nextTmp = -1;         // ids of nodes and links not saved yet
  var saving = false, dirty = false, saveTimer = null;

  function T(s) { return (meta && meta.text[s]) || s; }
  function el(name, attrs, parent) {
    var e = document.createElementNS(NS, name);
    for (var k in attrs) if (attrs[k] !== undefined && attrs[k] !== null) e.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(e);
    return e;
  }
  function h(tag, attrs, text) {
    var e = document.createElement(tag);
    for (var k in attrs || {}) e.setAttribute(k, attrs[k]);
    if (text !== undefined) e.textContent = text;
    return e;
  }
  function nodeByID(id) { for (var i = 0; i < layout.nodes.length; i++) if (layout.nodes[i].id === id) return layout.nodes[i]; return null; }
  function linkByID(id) { for (var i = 0; i < layout.links.length; i++) if (layout.links[i].id === id) return layout.links[i]; return null; }
  function bits(v) {
    if (v === null || v === undefined) return "—";
    var u = ["bit/s", "kbit/s", "Mbit/s", "Gbit/s", "Tbit/s"], i = 0;
    while (v >= 1000 && i < u.length - 1) { v /= 1000; i++; }
    return (v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)) + " " + u[i];
  }
  function store(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch (e) { /* private mode */ } }
  function load(k) { try { return JSON.parse(localStorage.getItem(k) || "null"); } catch (e) { return null; } }

  // ---- icons of device kinds (24×24, stroked)
  var icons = {
    router: "M4 15h16v4H4zM7.5 17h.01M11 17h.01M8.5 11.5a5 5 0 0 1 7 0M6 9a8.5 8.5 0 0 1 12 0",
    "switch": "M4 9h13l-3-3M20 15H7l3 3",
    firewall: "M3 5h18v14H3zM3 10h18M3 15h18M9 5v5M15 10v5M9 15v4",
    server: "M4 4h16v7H4zM4 13h16v7H4zM8 7.5h.01M8 16.5h.01",
    pc: "M3 4h18v12H3zM8 20h8M12 16v4",
    printer: "M6 9V3h12v6M6 18H4v-7h16v7h-2M6 14h12v7H6z",
    ups: "M3 7h16v10H3zM21 10v4M11 9l-2 4h4l-2 4",
    accesspoint: "M12 18h.01M8.5 14.5a5 5 0 0 1 7 0M5 11a10 10 0 0 1 14 0",
    camera: "M3 8h13v9H3zM16 11l5-3v9l-5-3",
    other: "M12 3a9 9 0 1 0 .01 0M12 8v5M12 16h.01",
    submap: "M9 4 3 6v14l6-2 6 2 6-2V4l-6 2-6-2ZM9 4v14M15 6v14"
  };

  // ---- view: world → screen is p·k + (x, y)
  function applyView() {
    vp.setAttribute("transform", "translate(" + view.x + " " + view.y + ") scale(" + view.k + ")");
    svg.classList.toggle("far", view.k < 0.6);
    svg.classList.toggle("very-far", view.k < 0.32);
    store("wz-map-view-" + mapID, view);
  }
  function toWorld(px, py) { return { x: (px - view.x) / view.k, y: (py - view.y) / view.k }; }
  function pointer(ev) { var r = svg.getBoundingClientRect(); return { x: ev.clientX - r.left, y: ev.clientY - r.top }; }
  function zoomAt(px, py, factor) {
    var k = Math.min(4, Math.max(0.08, view.k * factor));
    factor = k / view.k;
    view.x = px - (px - view.x) * factor;
    view.y = py - (py - view.y) * factor;
    view.k = k;
    applyView();
  }
  // smooth animation for the buttons and "fit"
  function animateTo(target) {
    var from = { x: view.x, y: view.y, k: view.k }, t0 = performance.now();
    function step(t) {
      var p = Math.min(1, (t - t0) / 220), e = p * (2 - p);
      view.x = from.x + (target.x - from.x) * e;
      view.y = from.y + (target.y - from.y) * e;
      view.k = from.k + (target.k - from.k) * e;
      applyView();
      if (p < 1) requestAnimationFrame(step);
    }
    requestAnimationFrame(step);
  }
  function fit(animate) {
    var r = svg.getBoundingClientRect();
    if (!layout.nodes.length) { var t = { x: r.width / 2, y: r.height / 2, k: 1 }; if (animate) animateTo(t); else { view = t; applyView(); } return; }
    var x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    layout.nodes.forEach(function (n) { x0 = Math.min(x0, n.x - 60); y0 = Math.min(y0, n.y - 50); x1 = Math.max(x1, n.x + 60); y1 = Math.max(y1, n.y + 70); });
    var k = Math.min(1.6, Math.max(0.08, Math.min(r.width / (x1 - x0), r.height / (y1 - y0))));
    var target = { k: k, x: r.width / 2 - (x0 + x1) / 2 * k, y: r.height / 2 - (y0 + y1) / 2 * k };
    if (animate) animateTo(target); else { view = target; applyView(); }
  }

  // ---- state of things
  function deviceStatus(n) {
    if (n.kind === "device") { var d = live.devices[n.device]; return d ? d.status : "unknown"; }
    if (n.kind === "submap") return live.maps[n.submap] || "unknown";
    return "";
  }
  function nodeName(n) {
    if (n.kind === "device") {
      var d = live.devices[n.device] || deviceMeta(n.device);
      return n.label || (d ? d.name : "?");
    }
    if (n.kind === "submap") { var m = mapMeta(n.submap); return n.label || (m ? m.name : "?"); }
    return n.label;
  }
  function deviceMeta(id) { var ds = (meta && meta.devices) || []; for (var i = 0; i < ds.length; i++) if (ds[i].id === id) return ds[i]; return null; }
  function mapMeta(id) { var ms = (meta && meta.maps) || []; for (var i = 0; i < ms.length; i++) if (ms[i].id === id) return ms[i]; return null; }
  function port(n, ifIndex) {
    if (!n || n.kind !== "device" || !ifIndex) return null;
    var ps = live.ports[n.device];
    return ps ? ps[ifIndex] || null : null;
  }
  // linkState returns the traffic in both directions and how the link looks.
  function linkState(k) {
    var a = nodeByID(k.a), b = nodeByID(k.b);
    var pa = port(a, k.a_if), pb = port(b, k.b_if);
    var ab = null, ba = null, speed = 0, down = false, known = !!(pa || pb);
    if (pa) { ab = pa.out; ba = pa.in; speed = pa.speed || 0; if (pa.admin && !pa.up) down = true; }
    if (pb) { if (ab === null || ab === undefined) ab = pb.in; if (ba === null || ba === undefined) ba = pb.out; speed = speed || pb.speed || 0; if (pb.admin && !pb.up) down = true; }
    var util = 0;
    if (speed > 0) util = Math.max(ab || 0, ba || 0) / speed;
    var sa = deviceStatus(a), sb = deviceStatus(b);
    if (!known && sa === "down" && sb === "down") down = true;
    return { a: pa, b: pb, ab: ab, ba: ba, speed: speed, util: util, down: down, known: known };
  }

  // ---- drawing
  function render() {
    while (gLinks.firstChild) gLinks.removeChild(gLinks.firstChild);
    while (gNodes.firstChild) gNodes.removeChild(gNodes.firstChild);
    layout.links.forEach(drawLink);
    layout.nodes.forEach(drawNode);
    root.classList.toggle("editing", editing);
    var del = root.querySelector('[data-act="delete"]');
    if (del) del.disabled = !selected;
  }

  function drawNode(n) {
    var st = deviceStatus(n);
    var g = el("g", { "class": "node n-" + n.kind + (st ? " s-" + st : "") + (selected && selected.type === "node" && selected.id === n.id ? " sel" : "") + (connectFrom === n.id ? " from" : ""),
      transform: "translate(" + n.x + " " + n.y + ")", "data-node": n.id }, gNodes);
    if (n.kind === "label") {
      el("text", { "class": "maplabel", y: 6, "text-anchor": "middle" }, g).textContent = n.label;
      return;
    }
    if (st === "down") el("circle", { "class": "pulse", r: 34 }, g);
    el("rect", { "class": "ring", x: -30, y: -30, width: 60, height: 60, rx: 18 }, g);
    el("rect", { "class": "body", x: -24, y: -24, width: 48, height: 48, rx: 14 }, g);
    var kind = n.kind === "submap" ? "submap" : ((live.devices[n.device] || deviceMeta(n.device) || {}).kind || "other");
    el("path", { "class": "glyph", d: icons[kind] || icons.other, transform: "translate(-12 -12)" }, g);
    var name = el("text", { "class": "name", y: 44, "text-anchor": "middle" }, g);
    name.textContent = nodeName(n);
    var d = n.kind === "device" ? live.devices[n.device] || deviceMeta(n.device) : null;
    if (d) {
      var sub = el("text", { "class": "addr", y: 60, "text-anchor": "middle" }, g);
      sub.textContent = d.address + (d.down ? "  · " + d.down + " " + T("down") : "");
    }
    var title = el("title", {}, g);
    title.textContent = nodeName(n) + (st ? " — " + T(st.charAt(0).toUpperCase() + st.slice(1)) : "");
  }

  function drawLink(k) {
    var a = nodeByID(k.a), b = nodeByID(k.b);
    if (!a || !b) return;
    var s = linkState(k);
    var cls = "link k-" + k.kind + (s.down ? " down" : "") + (selected && selected.type === "link" && selected.id === k.id ? " sel" : "");
    if (!s.down && s.known) cls += s.util > 0.8 ? " u3" : s.util > 0.5 ? " u2" : s.util > 0.05 ? " u1" : " u0";
    var g = el("g", { "class": cls, "data-link": k.id }, gLinks);
    var w = 2 + Math.min(7, s.util * 9);
    el("line", { "class": "hit", x1: a.x, y1: a.y, x2: b.x, y2: b.y }, g);
    el("line", { "class": "wire", x1: a.x, y1: a.y, x2: b.x, y2: b.y, "stroke-width": w.toFixed(1) }, g);
    var dx = b.x - a.x, dy = b.y - a.y, len = Math.sqrt(dx * dx + dy * dy) || 1;
    function at(dist) { return { x: a.x + dx / len * dist, y: a.y + dy / len * dist }; }
    // port names near each end, traffic in the middle
    // below a device are its name and address: keep port names clear of them
    var down = dy / len, offA = Math.min(down > 0.45 ? 100 : 64, len * 0.4), offB = Math.min(-down > 0.45 ? 100 : 64, len * 0.4);
    if (s.a || k.a_if) { var pa = at(offA); label(g, "port", pa.x, pa.y, s.a ? s.a.name : "#" + k.a_if); }
    if (s.b || k.b_if) { var pb = at(len - offB); label(g, "port", pb.x, pb.y, s.b ? s.b.name : "#" + k.b_if); }
    var mid = at(len / 2);
    var text = k.label || "";
    // traffic only where it fits between the devices (the panel always shows it)
    if (s.ab !== null && s.ab !== undefined && len > 190) text = (text ? text + "  " : "") + "→ " + bits(s.ab) + "  ← " + bits(s.ba);
    if (text) label(g, "traffic", mid.x, mid.y, text);
  }
  function label(g, cls, x, y, text) {
    var t = el("text", { "class": "lbl " + cls, x: x, y: y + 4, "text-anchor": "middle" }, g);
    t.textContent = text;
  }

  // ---- side panel
  function closePanel() { panel.hidden = true; panel.innerHTML = ""; }
  function openPanel(title, status) {
    panel.innerHTML = "";
    var head = h("div", { "class": "mp-head" });
    var ht = h("b", {}, title);
    head.appendChild(ht);
    if (status) { var b = h("span", { "class": "badge " + badgeClass(status) }); b.appendChild(h("i")); b.appendChild(document.createTextNode(T(status.charAt(0).toUpperCase() + status.slice(1)))); head.appendChild(b); }
    var x = h("button", { type: "button", "class": "iconbtn mp-close", title: T("Close"), "aria-label": T("Close") }, "×");
    x.addEventListener("click", function () { selected = null; closePanel(); render(); });
    head.appendChild(x);
    panel.appendChild(head);
    panel.hidden = false;
    return panel;
  }
  function badgeClass(s) { return { up: "ok", down: "bad", warning: "warn", paused: "muted", unknown: "muted" }[s] || "muted"; }
  function row(dl, k, v) { dl.appendChild(h("dt", {}, k)); var dd = h("dd"); if (typeof v === "string") dd.textContent = v; else dd.appendChild(v); dl.appendChild(dd); }
  function btn(text, href, cls) { var a = h("a", { "class": "btn " + (cls || "secondary") + " sm", href: href }, text); return a; }

  function showNode(n) {
    var st = deviceStatus(n);
    if (n.kind === "label") {
      if (!editing) { closePanel(); return; }
      var p = openPanel(T("Text"));
      var inp = h("input", { value: n.label, maxlength: "200", "aria-label": T("Text") });
      inp.addEventListener("input", function () { n.label = inp.value; render(); changed(); });
      p.appendChild(inp);
      return;
    }
    var p2 = openPanel(nodeName(n), st);
    var dl = h("dl", { "class": "facts" });
    if (n.kind === "device") {
      var d = live.devices[n.device] || deviceMeta(n.device) || {};
      row(dl, T("Address"), d.address || "");
      row(dl, T("Kind"), T("kind:" + (d.kind || "other")));
      if (d.vendor) row(dl, T("Vendor"), d.vendor);
      if (d.checks !== undefined) row(dl, T("Checks"), d.checks + (d.down ? " · " + d.down + " " + T("down") : ""));
      p2.appendChild(dl);
      var ps = live.ports[n.device];
      if (ps) {
        var list = h("ul", { "class": "mp-ports" });
        Object.keys(ps).sort(function (x, y) { return x - y; }).forEach(function (idx) {
          var pp = ps[idx];
          var li = h("li", { "class": !pp.admin ? "off" : pp.up ? "up" : "down" });
          li.appendChild(h("b", {}, pp.name));
          li.appendChild(h("span", {}, pp.in !== undefined && pp.in !== null ? "↓ " + bits(pp.in) + "  ↑ " + bits(pp.out) : !pp.admin ? T("Disabled") : pp.up ? T("Up") : T("No link")));
          list.appendChild(li);
        });
        p2.appendChild(h("h3", {}, T("Ports")));
        p2.appendChild(list);
      }
      var acts = h("div", { "class": "row mt" });
      acts.appendChild(btn(T("Open device"), "/devices/" + n.device, "primary"));
      if (d.web) { var wa = btn(T("Open web interface"), d.web); wa.target = "_blank"; wa.rel = "noopener noreferrer"; acts.appendChild(wa); }
      p2.appendChild(acts);
    } else {
      p2.appendChild(dl);
      var acts2 = h("div", { "class": "row mt" });
      acts2.appendChild(btn(T("Open map"), "/maps/" + n.submap, "primary"));
      p2.appendChild(acts2);
    }
    if (editing) {
      var lab = h("label", {}, T("Label"));
      var li2 = h("input", { value: n.label || "", maxlength: "200", placeholder: nodeName(n) });
      li2.addEventListener("input", function () { n.label = li2.value; render(); changed(); });
      lab.appendChild(li2);
      p2.appendChild(lab);
    }
  }

  function portSelect(n, value, onChange) {
    var sel = h("select");
    sel.appendChild(h("option", { value: "0" }, T("Not set")));
    var ps = n && n.kind === "device" ? live.ports[n.device] || {} : {};
    Object.keys(ps).sort(function (x, y) { return x - y; }).forEach(function (idx) {
      var o = h("option", { value: idx }, ps[idx].name + (ps[idx].alias ? " · " + ps[idx].alias : ""));
      if (+idx === value) o.selected = true;
      sel.appendChild(o);
    });
    sel.disabled = !editing || !n || n.kind !== "device";
    sel.addEventListener("change", function () { onChange(+sel.value); });
    return sel;
  }

  function showLink(k) {
    var a = nodeByID(k.a), b = nodeByID(k.b), s = linkState(k);
    var p = openPanel(T("Link"), s.down ? "down" : s.known ? "up" : "");
    var dl = h("dl", { "class": "facts" });
    row(dl, nodeName(a), portSelect(a, k.a_if, function (v) { k.a_if = v; render(); changed(); }));
    row(dl, nodeName(b), portSelect(b, k.b_if, function (v) { k.b_if = v; render(); changed(); }));
    if (s.ab !== null && s.ab !== undefined) {
      row(dl, nodeName(a) + " → " + nodeName(b), bits(s.ab));
      row(dl, nodeName(b) + " → " + nodeName(a), bits(s.ba));
    }
    if (s.speed) row(dl, T("Speed"), bits(s.speed));
    p.appendChild(dl);
    if (editing) {
      var kl = h("label", {}, T("Kind"));
      var ks = h("select");
      [["wired", "Wired"], ["wireless", "Wireless"], ["vpn", "VPN"]].forEach(function (o) { var op = h("option", { value: o[0] }, T(o[1])); if (k.kind === o[0]) op.selected = true; ks.appendChild(op); });
      ks.addEventListener("change", function () { k.kind = ks.value; render(); changed(); });
      kl.appendChild(ks);
      p.appendChild(kl);
      var ll = h("label", {}, T("Label"));
      var li = h("input", { value: k.label || "", maxlength: "100" });
      li.addEventListener("input", function () { k.label = li.value; render(); changed(); });
      ll.appendChild(li);
      p.appendChild(ll);
    }
    var pa = s.a, acts = h("div", { "class": "row mt" });
    if (pa && pa.check) acts.appendChild(btn(T("Open device"), "/devices/" + a.device + "#graph-" + pa.check, "secondary"));
    else if (s.b && s.b.check) acts.appendChild(btn(T("Open device"), "/devices/" + b.device + "#graph-" + s.b.check, "secondary"));
    p.appendChild(acts);
  }

  function showSelection() {
    if (!selected) { closePanel(); return; }
    if (selected.type === "node") { var n = nodeByID(selected.id); if (n) showNode(n); else closePanel(); }
    else { var k = linkByID(selected.id); if (k) showLink(k); else closePanel(); }
  }

  // ---- editing
  function freeSpot() {
    var r = svg.getBoundingClientRect(), c = toWorld(r.width / 2, r.height / 2);
    for (var ring = 0; ring < 40; ring++) {
      for (var i = 0; i < Math.max(1, ring * 6); i++) {
        var a = i / Math.max(1, ring * 6) * Math.PI * 2;
        var x = Math.round((c.x + Math.cos(a) * ring * 110) / 10) * 10, y = Math.round((c.y + Math.sin(a) * ring * 110) / 10) * 10;
        var free = layout.nodes.every(function (n) { return Math.abs(n.x - x) > 100 || Math.abs(n.y - y) > 100; });
        if (free) return { x: x, y: y };
      }
    }
    return { x: Math.round(c.x), y: Math.round(c.y) };
  }
  function addNode(n, keepPanel) {
    var p = freeSpot();
    n.id = nextTmp--; n.x = p.x; n.y = p.y;
    layout.nodes.push(n);
    showHint("");
    if (!keepPanel) { selected = { type: "node", id: n.id }; render(); showSelection(); } else render();
    changed();
  }
  function removeSelected() {
    if (!editing || !selected) return;
    if (selected.type === "node") {
      var id = selected.id;
      layout.nodes = layout.nodes.filter(function (n) { return n.id !== id; });
      layout.links = layout.links.filter(function (k) { return k.a !== id && k.b !== id; });
    } else {
      var lid = selected.id;
      layout.links = layout.links.filter(function (k) { return k.id !== lid; });
    }
    selected = null;
    closePanel(); render(); changed();
  }
  function pickList(title, items, empty, onPick) {
    var old = panel.querySelector(".mp-search"), keep = old ? old.value : "";
    var p = openPanel(title);
    var q = h("input", { type: "search", placeholder: T("Search"), "aria-label": T("Search") });
    q.value = keep;
    q.className = "mp-search";
    var ul = h("ul", { "class": "mp-pick" });
    function fill() {
      ul.innerHTML = "";
      var f = q.value.toLowerCase(), n = 0;
      items.forEach(function (it) {
        if (f && (it.name + " " + (it.sub || "")).toLowerCase().indexOf(f) < 0) return;
        var b = h("button", { type: "button" });
        b.appendChild(h("b", {}, it.name));
        if (it.sub) b.appendChild(h("small", {}, it.sub));
        b.addEventListener("click", function () { onPick(it); });
        var li = h("li"); li.appendChild(b); ul.appendChild(li);
        n++;
      });
      if (!n) ul.appendChild(h("li", { "class": "muted small" }, empty));
    }
    q.addEventListener("input", fill);
    p.appendChild(q); p.appendChild(ul);
    fill();
    q.focus();
  }
  function pickDevice() {
    var on = {};
    layout.nodes.forEach(function (n) { if (n.kind === "device") on[n.device] = true; });
    var items = (meta.devices || []).filter(function (d) { return !on[d.id]; }).map(function (d) { return { id: d.id, name: d.name, sub: d.address + " · " + T("kind:" + d.kind) }; });
    pickList(T("Add device"), items, T("All devices are on this map."), function (it) { addNode({ kind: "device", device: it.id, label: "" }, true); pickDevice(); });
  }
  function pickSubmap() {
    var items = (meta.maps || []).filter(function (m) { return String(m.id) !== String(mapID); }).map(function (m) { return { id: m.id, name: m.name }; });
    pickList(T("Add submap"), items, T("No other maps yet."), function (it) { addNode({ kind: "submap", submap: it.id, label: "" }); });
  }
  function showHint(text) { hint.textContent = text || ""; hint.hidden = !text; }
  function setConnect(on) {
    connecting = on; connectFrom = null;
    var b = root.querySelector('[data-act="connect"]');
    if (b) b.classList.toggle("on", on);
    showHint(on ? T("Click the first device, then the second.") : editing && !layout.nodes.length ? T("Drag devices to place them. Use Connect to draw a link and choose the ports.") : "");
    render();
  }
  function connectClick(n) {
    if (n.kind === "label") return;
    if (connectFrom === null) { connectFrom = n.id; render(); return; }
    if (connectFrom === n.id) { connectFrom = null; render(); return; }
    var k = { id: nextTmp--, a: connectFrom, b: n.id, a_if: 0, b_if: 0, kind: "wired", label: "" };
    layout.links.push(k);
    setConnect(false);
    selected = { type: "link", id: k.id };
    render(); showSelection(); changed();
  }
  function suggestions() {
    var p = openPanel(T("Suggested links"));
    p.appendChild(h("p", { "class": "muted small" }, T("Neighbours found over LLDP and MikroTik discovery.")));
    var ul = h("ul", { "class": "mp-pick" });
    p.appendChild(ul);
    fetch("/maps/" + mapID + "/suggest", { credentials: "same-origin" }).then(function (r) { return r.json(); }).then(function (list) {
      var n = 0;
      (list || []).forEach(function (sg) {
        var na = null, nb = null;
        layout.nodes.forEach(function (x) { if (x.kind === "device" && x.device === sg.a) na = x; if (x.kind === "device" && x.device === sg.b) nb = x; });
        if (!na || !nb) return;
        var exists = layout.links.some(function (k) { return (k.a === na.id && k.b === nb.id) || (k.a === nb.id && k.b === na.id); });
        if (exists) return;
        var b = h("button", { type: "button" });
        b.appendChild(h("b", {}, nodeName(na) + " " + (sg.a_name || "") + "  ↔  " + nodeName(nb) + " " + (sg.b_name || "")));
        b.appendChild(h("small", {}, sg.via));
        b.addEventListener("click", function () {
          layout.links.push({ id: nextTmp--, a: na.id, b: nb.id, a_if: sg.a_if || 0, b_if: sg.b_if || 0, kind: "wired", label: "" });
          b.parentNode.removeChild(b);
          render(); changed();
        });
        var li = h("li"); li.appendChild(b); ul.appendChild(li); n++;
      });
      if (!n) ul.appendChild(h("li", { "class": "muted small" }, T("No suggestions.")));
    }).catch(function () { ul.appendChild(h("li", { "class": "muted small" }, T("No suggestions."))); });
  }

  // ---- saving: debounced; temporary ids are replaced by the server's
  function changed() {
    dirty = true;
    setSave(T("Saving…"));
    clearTimeout(saveTimer);
    saveTimer = setTimeout(save, 700);
  }
  function setSave(t) { saveState.textContent = t; saveState.hidden = !t; }
  function save() {
    if (saving) { saveTimer = setTimeout(save, 300); return; }
    saving = true; dirty = false;
    layout.nodes = layout.nodes.filter(function (n) { return n.kind !== "label" || (n.label && n.label.trim()); });
    var sent = JSON.parse(JSON.stringify(layout));
    fetch("/maps/" + mapID + "/layout", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "Accept": "application/json" }, body: JSON.stringify(sent) })
      .then(function (r) { return r.json().then(function (j) { if (!r.ok) throw new Error(j.error || r.status); return j; }); })
      .then(function (out) {
        var ids = {};
        sent.nodes.forEach(function (n, i) { if (out.nodes[i]) ids[n.id] = out.nodes[i].id; });
        var lids = {};
        sent.links.forEach(function (k, i) { if (out.links[i]) lids[k.id] = out.links[i].id; });
        layout.nodes.forEach(function (n) { if (ids[n.id] !== undefined) n.id = ids[n.id]; });
        layout.links.forEach(function (k) {
          if (ids[k.a] !== undefined) k.a = ids[k.a];
          if (ids[k.b] !== undefined) k.b = ids[k.b];
          if (lids[k.id] !== undefined) k.id = lids[k.id];
        });
        if (selected) { var m = selected.type === "node" ? ids : lids; if (m[selected.id] !== undefined) selected.id = m[selected.id]; }
        if (connectFrom !== null && ids[connectFrom] !== undefined) connectFrom = ids[connectFrom];
        saving = false;
        if (!dirty) setSave(T("Saved"));
        render();
        refresh();
      })
      .catch(function (e) { saving = false; setSave(T("Could not save. Reload the page.") + " " + (e.message || "")); });
  }

  // ---- pointer handling: drag nodes, pan, pinch, select
  var pointers = {}, drag = null, pinch = null, moved = false;
  svg.addEventListener("pointerdown", function (ev) {
    if (ev.button !== undefined && ev.button !== 0 && ev.pointerType === "mouse") return;
    pointers[ev.pointerId] = pointer(ev);
    svg.setPointerCapture(ev.pointerId);
    moved = false;
    var ids = Object.keys(pointers);
    if (ids.length === 2) {
      var p0 = pointers[ids[0]], p1 = pointers[ids[1]];
      pinch = { d: Math.hypot(p1.x - p0.x, p1.y - p0.y), k: view.k, cx: (p0.x + p1.x) / 2, cy: (p0.y + p1.y) / 2, vx: view.x, vy: view.y };
      drag = null;
      return;
    }
    var nodeEl = ev.target.closest ? ev.target.closest("[data-node]") : null;
    var linkEl = ev.target.closest ? ev.target.closest("[data-link]") : null;
    var p = pointer(ev);
    if (nodeEl) {
      var n = nodeByID(+nodeEl.getAttribute("data-node"));
      var w = toWorld(p.x, p.y);
      drag = { type: "node", n: n, dx: n.x - w.x, dy: n.y - w.y, sx: p.x, sy: p.y };
    } else if (linkEl) {
      drag = { type: "link", id: +linkEl.getAttribute("data-link"), sx: p.x, sy: p.y };
    } else {
      drag = { type: "pan", sx: p.x, sy: p.y, vx: view.x, vy: view.y };
    }
  });
  svg.addEventListener("pointermove", function (ev) {
    if (!pointers[ev.pointerId]) return;
    var p = pointer(ev);
    pointers[ev.pointerId] = p;
    if (pinch) {
      var ids = Object.keys(pointers);
      if (ids.length < 2) return;
      var p0 = pointers[ids[0]], p1 = pointers[ids[1]];
      var d = Math.hypot(p1.x - p0.x, p1.y - p0.y), cx = (p0.x + p1.x) / 2, cy = (p0.y + p1.y) / 2;
      var k = Math.min(4, Math.max(0.08, pinch.k * d / pinch.d));
      view.k = k;
      view.x = cx - (pinch.cx - pinch.vx) / pinch.k * k;
      view.y = cy - (pinch.cy - pinch.vy) / pinch.k * k;
      applyView();
      moved = true;
      return;
    }
    if (!drag) return;
    if (Math.abs(p.x - drag.sx) + Math.abs(p.y - drag.sy) > 4) moved = true;
    if (!moved) return;
    if (drag.type === "pan") {
      view.x = drag.vx + p.x - drag.sx;
      view.y = drag.vy + p.y - drag.sy;
      applyView();
    } else if (drag.type === "node" && editing && !connecting) {
      var w = toWorld(p.x, p.y);
      drag.n.x = Math.round((w.x + drag.dx) / 10) * 10;
      drag.n.y = Math.round((w.y + drag.dy) / 10) * 10;
      drag.changed = true;
      render();
    } else if (drag.type === "node") {
      // not editing: dragging a device pans the map
      if (!drag.vx && drag.vx !== 0) { drag.vx = view.x; drag.vy = view.y; }
      view.x = drag.vx + p.x - drag.sx;
      view.y = drag.vy + p.y - drag.sy;
      applyView();
    }
  });
  function endPointer(ev) {
    delete pointers[ev.pointerId];
    if (pinch) { if (Object.keys(pointers).length < 2) pinch = null; drag = null; return; }
    var d = drag;
    drag = null;
    if (!d) return;
    if (d.type === "node" && d.changed) { changed(); return; }
    if (moved) return;
    // a click
    if (d.type === "node") {
      if (connecting) { connectClick(d.n); return; }
      selected = { type: "node", id: d.n.id };
    } else if (d.type === "link") {
      selected = { type: "link", id: d.id };
    } else {
      selected = null;
    }
    render(); showSelection();
  }
  svg.addEventListener("pointerup", endPointer);
  svg.addEventListener("pointercancel", endPointer);
  svg.addEventListener("wheel", function (ev) {
    ev.preventDefault();
    var p = pointer(ev);
    var dy = ev.deltaMode === 1 ? ev.deltaY * 16 : ev.deltaY;
    zoomAt(p.x, p.y, Math.pow(1.0012, -dy));
  }, { passive: false });
  svg.addEventListener("dblclick", function (ev) {
    var nodeEl = ev.target.closest ? ev.target.closest("[data-node]") : null;
    if (!nodeEl || editing) { if (!nodeEl) { var p = pointer(ev); zoomAt(p.x, p.y, 1.6); } return; }
    var n = nodeByID(+nodeEl.getAttribute("data-node"));
    if (n && n.kind === "device") location.href = "/devices/" + n.device;
    if (n && n.kind === "submap") location.href = "/maps/" + n.submap;
  });
  document.addEventListener("keydown", function (ev) {
    if (ev.target && /INPUT|SELECT|TEXTAREA/.test(ev.target.tagName)) return;
    if ((ev.key === "Delete" || ev.key === "Backspace") && editing && selected) { ev.preventDefault(); removeSelected(); }
    if (ev.key === "Escape") { if (connecting) setConnect(false); else { selected = null; closePanel(); render(); } }
    if (ev.key === "+" || ev.key === "=") { var r = svg.getBoundingClientRect(); zoomAt(r.width / 2, r.height / 2, 1.25); }
    if (ev.key === "-") { var r2 = svg.getBoundingClientRect(); zoomAt(r2.width / 2, r2.height / 2, 0.8); }
  });

  // ---- toolbar
  root.addEventListener("click", function (ev) {
    var b = ev.target.closest("[data-act]");
    if (!b) return;
    var act = b.getAttribute("data-act"), r = svg.getBoundingClientRect();
    if (act === "zoom-in") animateTo({ k: Math.min(4, view.k * 1.4), x: r.width / 2 - (r.width / 2 - view.x) * Math.min(4, view.k * 1.4) / view.k, y: r.height / 2 - (r.height / 2 - view.y) * Math.min(4, view.k * 1.4) / view.k });
    else if (act === "zoom-out") { var k = Math.max(0.08, view.k / 1.4); animateTo({ k: k, x: r.width / 2 - (r.width / 2 - view.x) * k / view.k, y: r.height / 2 - (r.height / 2 - view.y) * k / view.k }); }
    else if (act === "fit") fit(true);
    else if (act === "edit") setEditing(!editing);
    else if (act === "add-device") { setConnect(false); pickDevice(); }
    else if (act === "add-submap") { setConnect(false); pickSubmap(); }
    else if (act === "add-text") { setConnect(false); addNode({ kind: "label", label: T("Text") }); }
    else if (act === "connect") setConnect(!connecting);
    else if (act === "suggest") { setConnect(false); suggestions(); }
    else if (act === "delete") removeSelected();
  });
  function setEditing(on) {
    editing = on && meta && meta.can_edit;
    Array.prototype.forEach.call(root.querySelectorAll("[data-edit-only]"), function (e) { e.hidden = !editing; });
    var l = root.querySelector("[data-edit-label]");
    if (l) l.textContent = editing ? T("Done") : T("Edit");
    if (!editing) setConnect(false);
    else showHint(!layout.nodes.length ? T("Drag devices to place them. Use Connect to draw a link and choose the ports.") : "");
    try { history.replaceState(null, "", location.pathname + (editing ? "?edit=1" : "")); } catch (e) { /* ignore */ }
    render(); showSelection();
  }

  // ---- data
  function refresh() {
    return fetch("/maps/" + mapID + "/live", { credentials: "same-origin", headers: { "Accept": "application/json" } })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
      .then(function (d) { live = d; if (!drag) { render(); if (selected && !panel.contains(document.activeElement)) showSelection(); } })
      .catch(function () { /* next round */ });
  }
  fetch("/maps/" + mapID + "/layout", { credentials: "same-origin", headers: { "Accept": "application/json" } })
    .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
    .then(function (d) {
      meta = d;
      layout = d.layout || { nodes: [], links: [] };
      var saved = load("wz-map-view-" + mapID);
      if (saved && isFinite(saved.k)) { view = saved; applyView(); } else fit(false);
      setEditing(editing);
      return refresh();
    })
    .then(function () { if (!load("wz-map-view-" + mapID)) fit(false); });
  setInterval(function () { if (!document.hidden && !saving) refresh(); }, 10000);
  // the map fills the window below the toolbar (banners above vary)
  function size() { root.style.height = Math.max(380, window.innerHeight - root.getBoundingClientRect().top - 14) + "px"; }
  size();
  window.addEventListener("resize", function () { size(); applyView(); });
})();

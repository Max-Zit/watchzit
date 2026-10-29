// WatchZit network map: a drawing of the network that is live monitoring.
// Devices are pictures (built-in drawings or uploaded ones) placed by drag
// and drop, mounted in racks, and linked port to port by cables of
// different kinds that can bend at 90° or 45°. Colours show their state,
// cable widths the traffic. Own SVG code (the CSP allows only scripts of
// this console): wheel and pinch zoom around the pointer, drag to pan,
// semantic zoom. The same code drives kiosk displays (read only, maps
// change by themselves).
(function () {
  "use strict";
  // the asset version this script was loaded with: built-in pictures and
  // their index carry it, so an update is seen at once despite the cache
  var assetV = (function () { var s = document.currentScript, m = s && /[?&]v=([^&]+)/.exec(s.src); return m ? m[1] : ""; })();
  var root = document.getElementById("mapview");
  if (!root) return;
  var kioskBase = root.getAttribute("data-kiosk");
  var kioskMaps = kioskBase ? (root.getAttribute("data-maps") || "").split(",").filter(Boolean) : [];
  var mapID = kioskBase ? kioskMaps[0] : root.getAttribute("data-map");
  var svg = root.querySelector("svg.mapsvg");
  var vp = svg.querySelector("g.viewport");
  var gBg = svg.querySelector("g.bg"), gRacks = svg.querySelector("g.racks"), gLinks = svg.querySelector("g.links");
  var gNodes = svg.querySelector("g.nodes"), gHandles = svg.querySelector("g.handles") || svg.querySelector("g.nodes");
  // cable labels (ports, traffic, VLANs) above all cables, so none hides them
  var gLabels = svg.querySelector("g.labels") || gLinks;
  var panel = root.querySelector("[data-panel]");
  var hint = root.querySelector("[data-hint]");
  var saveState = root.querySelector("[data-save]");
  var upload = root.querySelector("[data-upload]");
  var NS = "http://www.w3.org/2000/svg";

  var RACK_W = 240, UH = RACK_W * 44 / 482; // a 19" unit at the drawing's scale
  var meta = null;          // name, devices, maps, words, uploaded pictures
  var lib = {}, libList = []; // built-in pictures (static/devices/index.json)
  var layout = { nodes: [], links: [], background: null, ha: [] };
  var live = { devices: {}, ports: {}, maps: {} };
  var view = { x: 0, y: 0, k: 1 };
  var editing = !kioskBase && root.getAttribute("data-edit") === "1";
  var selected = null;      // {type: "node"|"link", id}
  var path = null;          // shown path: {nodes: {id: true}, links: {id: true}}
  var pathPick = null;      // picking the two ends of a path: {a}
  var connectFrom = null, connecting = false, moveBg = false;
  var nextTmp = -1;         // ids of nodes and links not saved yet
  var saving = false, dirty = false;

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
  function clear(g) { while (g.firstChild) g.removeChild(g.firstChild); }
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
  function cap(s) { return s ? s.charAt(0).toUpperCase() + s.slice(1) : ""; }
  function url(kind, id) {
    if (kioskBase) return kioskBase + "/" + kind + "/" + id;
    if (kind === "assets") return "/assets/" + id;
    return "/maps/" + id + "/" + kind;
  }

  // ---- kind symbols (24×24, stroked) for nodes without a picture
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
    svg.classList.toggle("near", view.k > 1.6);
    svg.classList.toggle("close", view.k > 1.2); // port names on cables
    if (!kioskBase) store("wz-map-view-" + mapID, view);
  }
  function toWorld(px, py) { return { x: (px - view.x) / view.k, y: (py - view.y) / view.k }; }
  function pointer(ev) { var r = svg.getBoundingClientRect(); return { x: ev.clientX - r.left, y: ev.clientY - r.top }; }
  function zoomAt(px, py, factor) {
    var k = Math.min(8, Math.max(0.05, view.k * factor));
    factor = k / view.k;
    view.x = px - (px - view.x) * factor;
    view.y = py - (py - view.y) * factor;
    view.k = k;
    applyView();
  }
  function animateTo(target) {
    var from = { x: view.x, y: view.y, k: view.k }, t0 = performance.now();
    function step(t) {
      var p = Math.min(1, (t - t0) / 240), e = p * (2 - p);
      view.x = from.x + (target.x - from.x) * e;
      view.y = from.y + (target.y - from.y) * e;
      view.k = from.k + (target.k - from.k) * e;
      applyView();
      if (p < 1) requestAnimationFrame(step);
    }
    requestAnimationFrame(step);
  }
  function bounds() {
    var x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    layout.nodes.forEach(function (n) {
      if (path && !path.nodes[n.id]) return; // showing a path: fit it
      var b = box(n);
      x0 = Math.min(x0, n.x - b.w / 2 - 20); y0 = Math.min(y0, n.y - b.h / 2 - 30);
      x1 = Math.max(x1, n.x + b.w / 2 + (rackOf(n) ? 110 : 20)); y1 = Math.max(y1, n.y + b.h / 2 + 50); // mounted: name on the right
    });
    var bg = layout.background;
    if (bg && bg.asset && !layout.nodes.length) { x0 = bg.x; y0 = bg.y; x1 = bg.x + bg.w; y1 = bg.y + bgHeight(); }
    return isFinite(x0) ? { x0: x0, y0: y0, x1: x1, y1: y1 } : null;
  }
  function fit(animate) {
    var r = svg.getBoundingClientRect(), b = bounds();
    var t;
    if (!b) t = { x: r.width / 2, y: r.height / 2, k: 1 };
    else {
      var k = Math.min(2, Math.max(0.05, Math.min(r.width / (b.x1 - b.x0), r.height / (b.y1 - b.y0))));
      t = { k: k, x: r.width / 2 - (b.x0 + b.x1) / 2 * k, y: r.height / 2 - (b.y0 + b.y1) / 2 * k };
    }
    if (animate) animateTo(t); else { view = t; applyView(); }
  }

  // ---- pictures and sizes
  function picOf(n) {
    if (!n.icon) return null;
    if (n.icon.indexOf("lib:") === 0) {
      // a built-in picture, or the version of it changed in the picture library
      var rid = replacement(n.icon.slice(4));
      return rid ? assetPic(rid) : lib[n.icon.slice(4)] || null;
    }
    if (n.icon.indexOf("asset:") === 0) return assetPic(n.icon.slice(6));
    return null;
  }
  // assetPic: an uploaded picture with what the picture library says about it
  function assetPic(id) {
    var a = meta && meta.assets[id], m = (a && a.meta) || {};
    return { url: url("assets", id), w: (a && a.w) || 100, h: (a && a.h) || 100, show: m.show || 80, ports: m.ports || [], u: m.u || 0, fiber: m.fiber || 0, label: a ? a.name : "" };
  }
  function replacement(name) {
    var as = (meta && meta.assets) || {};
    for (var id in as) if (as[id].meta && as[id].meta.replaces === name) return id;
    return "";
  }
  function picURL(p) { return p.url || "/static/devices/" + p.name + ".svg?v=" + assetV; }
  function unitsOf(n) { var p = picOf(n); return p && p.u ? p.u : 1; }
  // box is the drawn size of a node in world units
  function box(n) {
    if (n.kind === "rack") return { w: RACK_W + 34, h: n.u * UH + 40 };
    if (n.kind === "host") {
      var c = guestsOf(n).length, cols = hostCols(c), rows = Math.max(1, Math.ceil(c / cols));
      return { w: Math.max(280, cols * HOST_TILE_W + 20), h: HOST_HEAD + rows * HOST_TILE_H + 12 };
    }
    var p = picOf(n);
    if (rackOf(n)) return { w: RACK_W, h: unitsOf(n) * UH, pic: p && p.u ? p : null };
    if (p && hostOf(n)) { var s0 = Math.min(90 / p.w, 70 / p.h); return { w: p.w * s0, h: p.h * s0, pic: p }; }
    if (n.kind === "label") return { w: Math.max(40, (n.label || "").length * 9), h: 20 };
    if (p) { var w = n.size || p.show || 80; return { w: w, h: w * p.h / p.w, pic: p }; }
    var s = n.size || 48;
    return { w: s, h: s };
  }
  function rackTop(r) { return r.y - r.u * UH / 2; }
  // hypervisor hosts: a box with the virtual machines (and devices) that
  // run on it laid out in a grid, in the order of their place (u)
  var HOST_TILE_W = 130, HOST_TILE_H = 115, HOST_HEAD = 56;
  function rackOf(n) { var r = n.parent && nodeByID(n.parent); return r && r.kind === "rack" ? r : null; }
  function hostOf(n) { var r = n.parent && nodeByID(n.parent); return r && r.kind === "host" ? r : null; }
  function guestsOf(h) {
    return layout.nodes.filter(function (c) { return c.parent === h.id; }).sort(function (a, b) { return (a.u || 0) - (b.u || 0) || a.id - b.id; });
  }
  function hostCols(n) { return n <= 1 ? 1 : n <= 4 ? 2 : n <= 9 ? 3 : 4; }
  // place puts mounted nodes into their rack slots and guests into their host
  function place() {
    layout.nodes.forEach(function (n) {
      if (!n.parent) return;
      var r = nodeByID(n.parent);
      if (!r) { n.parent = 0; return; }
      if (r.kind === "host") {
        var gs = guestsOf(r), i = gs.indexOf(n), cols = hostCols(gs.length), b = box(r);
        n.x = r.x - b.w / 2 + 10 + (i % cols) * HOST_TILE_W + HOST_TILE_W / 2;
        n.y = r.y - b.h / 2 + HOST_HEAD + Math.floor(i / cols) * HOST_TILE_H + 42;
        return;
      }
      var u = unitsOf(n);
      n.x = r.x;
      n.y = rackTop(r) + (r.u - (n.u + u - 1)) * UH + u * UH / 2;
    });
  }
  // anchor is where a cable ends: the port on the picture, else the centre
  function anchor(n, slot) {
    var b = box(n);
    if (b.pic && b.pic.ports && slot >= 1 && slot <= b.pic.ports.length) {
      var p = b.pic.ports[slot - 1], s = b.w / b.pic.w;
      return { x: n.x - b.w / 2 + p[0] * s, y: n.y - b.h / 2 + p[1] * s, port: true };
    }
    return { x: n.x, y: n.y };
  }
  function bgHeight() {
    var bg = layout.background, a = bg && meta && meta.assets[bg.asset];
    return a && a.w && a.h ? bg.w * a.h / a.w : bg ? bg.w * 0.66 : 0;
  }

  // ---- state of things
  var rank = { paused: 0, up: 1, unknown: 2, warning: 3, down: 4 };
  function worse(a, b) { return (rank[b] || 0) > (rank[a] || 0) ? b : a || b; }
  function nodeStatus(n) {
    if (n.kind === "device") { var d = live.devices[n.device]; return d ? d.status : "unknown"; }
    if (n.kind === "submap") return live.maps[n.submap] || "unknown";
    if (n.kind === "rack" || n.kind === "host") {
      var w = "";
      layout.nodes.forEach(function (c) { if (c.parent === n.id && c.kind === "device") w = worse(w, nodeStatus(c)); });
      if (n.kind === "host") [n.host_device].concat(n.host_devices || []).forEach(function (id) { if (id) { var hd = live.devices[id]; w = worse(w, hd ? hd.status : "unknown"); } });
      return w;
    }
    return "";
  }
  function deviceMeta(id) { var ds = (meta && meta.devices) || []; for (var i = 0; i < ds.length; i++) if (ds[i].id === id) return ds[i]; return null; }
  function mapMeta(id) { var ms = (meta && meta.maps) || []; for (var i = 0; i < ms.length; i++) if (ms[i].id === id) return ms[i]; return null; }
  function nodeName(n) {
    if (n.kind === "device") { var d = live.devices[n.device] || deviceMeta(n.device); return n.label || (d ? d.name : "?"); }
    if (n.kind === "submap") { var m = mapMeta(n.submap); return n.label || (m ? m.name : "?"); }
    if (n.kind === "item" && !n.label) { var p = picOf(n); return p && p.label ? p.label : ""; }
    if (n.kind === "host" && !n.label) {
      var hd = n.host_device && (live.devices[n.host_device] || deviceMeta(n.host_device));
      if (hd) return hd.name;
      var hp = picOf(n);
      return hp && hp.label ? hp.label : T("Hypervisor");
    }
    return n.label || "";
  }
  function port(n, ifIndex) {
    if (!n || n.kind !== "device" || !ifIndex) return null;
    var ps = live.ports[n.device];
    return ps ? ps[ifIndex] || null : null;
  }
  function linkState(k) {
    var a = nodeByID(k.a), b = nodeByID(k.b);
    var pa = port(a, k.a_if), pb = port(b, k.b_if);
    var ab = null, ba = null, speed = 0, down = false, known = !!(pa || pb);
    if (pa) { ab = pa.out; ba = pa.in; speed = pa.speed || 0; if (pa.admin && !pa.up) down = true; }
    if (pb) { if (ab === null || ab === undefined) ab = pb.in; if (ba === null || ba === undefined) ba = pb.out; speed = speed || pb.speed || 0; if (pb.admin && !pb.up) down = true; }
    var util = speed > 0 ? Math.max(ab || 0, ba || 0) / speed : 0;
    if (!known && nodeStatus(a) === "down" && nodeStatus(b) === "down") down = true;
    return { a: pa, b: pb, ab: ab, ba: ba, speed: speed, util: util, down: down, known: known };
  }
  function slotName(n, slot) {
    var p = box(n).pic;
    if (!p || !slot) return "";
    var copper = p.ports.length - (p.fiber || 0);
    return slot > copper ? "SFP " + (slot - copper) : String(slot);
  }
  // guessSlot finds the picture port of an interface name (ether3 → 3,
  // sfp1 → first SFP cage, eth0 → 1).
  function guessSlot(n, name) {
    var p = box(n).pic;
    if (!p || !p.ports || !p.ports.length || !name) return 0;
    var m = /(\d+)\D*$/.exec(name);
    if (!m) return 0;
    var num = parseInt(m[1], 10), copper = p.ports.length - (p.fiber || 0);
    if (/^eth\d/i.test(name)) num += 1;
    if (/sfp|xe-|te\d/i.test(name) && p.fiber) num = copper + num;
    return num >= 1 && num <= p.ports.length ? num : 0;
  }

  // slotOf is the picture port a cable ends in: the chosen one, else the one
  // its interface name points to (ether2 → port 2)
  function slotOf(k, side) {
    if (k[side + "_slot"]) return k[side + "_slot"];
    var n = nodeByID(k[side]), p = port(n, k[side + "_if"]);
    return p ? guessSlot(n, p.name) : 0;
  }

  // ---- cable geometry: corners for the route, sampled curves
  function routePoints(k) {
    var a = nodeByID(k.a), b = nodeByID(k.b);
    var pa = anchor(a, slotOf(k, "a")), pb = anchor(b, slotOf(k, "b"));
    var raw = [pa].concat((k.points || []).map(function (p) { return { x: p[0], y: p[1] }; })).concat([pb]);
    // without a port the cable stops at the edge of the picture
    if (!pa.port) raw[0] = edgePoint(a, raw[1]);
    if (!pb.port) raw[raw.length - 1] = edgePoint(b, raw[raw.length - 2]);
    var route = k.route || "straight";
    if (route === "straight") return raw;
    var out = [raw[0]];
    if (route === "curve") {
      if (raw.length === 2) {
        var mx = (pa.x + pb.x) / 2;
        for (var t = 1; t <= 24; t++) out.push(bezier(pa, { x: mx, y: pa.y }, { x: mx, y: pb.y }, pb, t / 24));
        return out;
      }
      for (var i = 0; i < raw.length - 1; i++) {
        var p0 = raw[Math.max(0, i - 1)], p1 = raw[i], p2 = raw[i + 1], p3 = raw[Math.min(raw.length - 1, i + 2)];
        for (var s = 1; s <= 16; s++) {
          var u = s / 16, u2 = u * u, u3 = u2 * u;
          out.push({
            x: 0.5 * (2 * p1.x + (-p0.x + p2.x) * u + (2 * p0.x - 5 * p1.x + 4 * p2.x - p3.x) * u2 + (-p0.x + 3 * p1.x - 3 * p2.x + p3.x) * u3),
            y: 0.5 * (2 * p1.y + (-p0.y + p2.y) * u + (2 * p0.y - 5 * p1.y + 4 * p2.y - p3.y) * u2 + (-p0.y + 3 * p1.y - 3 * p2.y + p3.y) * u3)
          });
        }
      }
      return out;
    }
    for (var j = 1; j < raw.length; j++) {
      var q = raw[j - 1], r = raw[j], dx = r.x - q.x, dy = r.y - q.y;
      if (route === "ortho") {
        if (raw.length === 2) {
          // Z shape through the middle, starting the way the gap is wider
          if (Math.abs(dx) >= Math.abs(dy)) { var x2 = q.x + dx / 2; out.push({ x: x2, y: q.y }, { x: x2, y: r.y }); }
          else { var y2 = q.y + dy / 2; out.push({ x: q.x, y: y2 }, { x: r.x, y: y2 }); }
        } else out.push({ x: r.x, y: q.y });
      } else { // diagonal: straight, then 45°
        var m2 = Math.min(Math.abs(dx), Math.abs(dy));
        if (Math.abs(dx) > Math.abs(dy)) out.push({ x: r.x - Math.sign(dx) * m2, y: q.y });
        else out.push({ x: q.x, y: r.y - Math.sign(dy) * m2 });
      }
      out.push(r);
    }
    return out;
  }
  function edgePoint(n, to) {
    var b = box(n), dx = to.x - n.x, dy = to.y - n.y;
    if (!dx && !dy) return { x: n.x, y: n.y };
    var hw = b.w / 2 + 6, hh = b.h / 2 + 6;
    var s = Math.min(dx ? hw / Math.abs(dx) : Infinity, dy ? hh / Math.abs(dy) : Infinity);
    if (s >= 1) return { x: n.x, y: n.y };
    return { x: n.x + dx * s, y: n.y + dy * s };
  }
  function bezier(a, b, c, d, t) {
    var u = 1 - t;
    return { x: u * u * u * a.x + 3 * u * u * t * b.x + 3 * u * t * t * c.x + t * t * t * d.x, y: u * u * u * a.y + 3 * u * u * t * b.y + 3 * u * t * t * c.y + t * t * t * d.y };
  }
  function pathD(pts) { return pts.map(function (p, i) { return (i ? "L" : "M") + p.x.toFixed(1) + " " + p.y.toFixed(1); }).join(""); }
  function lengthOf(pts) { var l = 0; for (var i = 1; i < pts.length; i++) l += Math.hypot(pts[i].x - pts[i - 1].x, pts[i].y - pts[i - 1].y); return l; }
  function pointAlong(pts, dist) {
    for (var i = 1; i < pts.length; i++) {
      var seg = Math.hypot(pts[i].x - pts[i - 1].x, pts[i].y - pts[i - 1].y);
      if (dist <= seg || i === pts.length - 1) {
        var t = seg ? Math.min(1, dist / seg) : 0;
        return { x: pts[i - 1].x + (pts[i].x - pts[i - 1].x) * t, y: pts[i - 1].y + (pts[i].y - pts[i - 1].y) * t };
      }
      dist -= seg;
    }
    return pts[0];
  }

  // ---- drawing
  function render() {
    place();
    [gBg, gRacks, gLinks, gNodes].forEach(clear);
    if (gLabels !== gLinks) clear(gLabels);
    if (gHandles !== gNodes) clear(gHandles);
    drawBackground();
    layout.nodes.forEach(function (n) { if (n.kind === "rack") drawRack(n); else if (n.kind === "host") drawHost(n); });
    layout.ha.forEach(drawHA);
    layout.links.forEach(drawLink);
    drawTunnels();
    drawHostLinks();
    layout.nodes.forEach(function (n) { if (n.kind !== "rack" && n.kind !== "host") drawNode(n); });
    drawHandles();
    svg.classList.toggle("pathmode", !!path);
    svg.classList.toggle("vlanmode", !!vlan);
    root.classList.toggle("editing", editing);
    var del = root.querySelector('[data-act="delete"]');
    if (del) del.disabled = !selected;
  }

  function drawBackground() {
    var bg = layout.background;
    if (!bg || !bg.asset) return;
    el("image", { href: url("assets", bg.asset), x: bg.x, y: bg.y, width: bg.w, height: bgHeight(), opacity: bg.opacity, preserveAspectRatio: "none", "class": "bgimg" + (moveBg && editing ? " movable" : "") }, gBg);
  }

  function drawRack(n) {
    var st = nodeStatus(n), b = box(n), top = rackTop(n);
    var sel = selected && selected.type === "node" && selected.id === n.id;
    var g = el("g", { "class": "rack" + (st ? " s-" + st : "") + (sel ? " sel" : "") + onPath("nodes", n.id), "data-node": n.id }, gRacks);
    el("rect", { "class": "cab", x: n.x - b.w / 2, y: top - 30, width: b.w, height: n.u * UH + 40, rx: 6 }, g);
    el("rect", { "class": "inner", x: n.x - RACK_W / 2, y: top, width: RACK_W, height: n.u * UH }, g);
    for (var u = 1; u <= n.u; u++) {
      var y = top + (n.u - u) * UH;
      el("line", { "class": "uline", x1: n.x - RACK_W / 2, y1: y, x2: n.x + RACK_W / 2, y2: y }, g);
      el("text", { "class": "unum", x: n.x - RACK_W / 2 - 4, y: y + UH / 2 + 3, "text-anchor": "end" }, g).textContent = u;
    }
    el("text", { "class": "rname", x: n.x, y: top - 12, "text-anchor": "middle" }, g).textContent = (n.label || T("Rack")) + " · " + n.u + "U";
  }

  // drawHost: a hypervisor host — its type and name on top, the virtual
  // machines inside (drawn as nodes of their own)
  function drawHost(n) {
    var st = nodeStatus(n), b = box(n), x0 = n.x - b.w / 2, y0 = n.y - b.h / 2;
    var sel = selected && selected.type === "node" && selected.id === n.id;
    var g = el("g", { "class": "host" + (st ? " s-" + st : "") + (sel ? " sel" : "") + onPath("nodes", n.id), "data-node": n.id }, gRacks);
    el("rect", { "class": "hostbox", x: x0, y: y0, width: b.w, height: b.h, rx: 10 }, g);
    var p = picOf(n);
    if (p) el("image", { href: picURL(p), x: x0 + 10, y: y0 + 8, width: 120, height: 32 }, g);
    el("text", { "class": "hname", x: x0 + (p ? 140 : 14), y: y0 + 29 }, g).textContent = nodeName(n);
    if (st) el("circle", { "class": "dot", cx: x0 + b.w - 14, cy: y0 + 24, r: 6 }, g);
    if (!guestsOf(n).length) el("text", { "class": "hhint", x: n.x, y: y0 + HOST_HEAD + 50, "text-anchor": "middle" }, g).textContent = editing ? T("Drag virtual machines here.") : T("No virtual machines yet.");
    el("title", {}, g).textContent = nodeName(n) + (st ? " — " + T(cap(st)) : "");
  }

  // haRole: ACTIVE or STANDBY over a device that reports its HA role (VRRP)
  function haRole(g, n, x, y, anchor) {
    var d = n.kind === "device" ? live.devices[n.device] : null;
    if (!d || !d.ha) return;
    el("text", { "class": "harole r-" + d.ha, x: x, y: y, "text-anchor": anchor }, g).textContent = d.ha === "master" ? T("ACTIVE") : T("STANDBY");
  }
  function drawNode(n) {
    var st = nodeStatus(n), b = box(n);
    var sel = selected && selected.type === "node" && selected.id === n.id;
    var g = el("g", { "class": "node n-" + n.kind + (st ? " s-" + st : "") + (sel ? " sel" : "") + (connectFrom === n.id ? " from" : "") + (rackOf(n) ? " mounted" : "") + (hostOf(n) ? " guest" : "") + onPath("nodes", n.id) + (vlan && deviceInVLAN(n, vlan) ? " invlan" : ""),
      transform: "translate(" + n.x + " " + n.y + ")", "data-node": n.id }, gNodes);
    if (n.kind === "label") {
      el("text", { "class": "maplabel", y: 6, "text-anchor": "middle" }, g).textContent = n.label;
      return;
    }
    var p = picOf(n), name = nodeName(n);
    if (rackOf(n)) {
      // mounted: the front panel fills its units; a state stripe on the left
      if (b.pic) el("image", { href: picURL(b.pic), x: -b.w / 2, y: -b.h / 2, width: b.w, height: b.h, preserveAspectRatio: "none" }, g);
      else {
        el("rect", { "class": "panel1u", x: -b.w / 2, y: -b.h / 2, width: b.w, height: b.h }, g);
        if (p) el("image", { href: picURL(p), x: -b.w / 2 + 4, y: -b.h / 2 + 1, width: b.h * 1.6, height: b.h - 2 }, g);
      }
      el("rect", { "class": "stripe", x: -b.w / 2 - 7, y: -b.h / 2 + 1, width: 4, height: b.h - 2, rx: 2 }, g);
      el("rect", { "class": "ring", x: -b.w / 2 - 2, y: -b.h / 2 - 1, width: b.w + 4, height: b.h + 2, rx: 2 }, g);
      el("text", { "class": "name side", x: b.w / 2 + 10, y: 4 }, g).textContent = name;
      haRole(g, n, b.w / 2 + 10, -10, "start");
    } else if (p) {
      if (st === "down") el("rect", { "class": "pulse", x: -b.w / 2 - 8, y: -b.h / 2 - 8, width: b.w + 16, height: b.h + 16, rx: 12 }, g);
      el("rect", { "class": "frame", x: -b.w / 2 - 5, y: -b.h / 2 - 5, width: b.w + 10, height: b.h + 10, rx: 8 }, g);
      el("image", { href: picURL(p), x: -b.w / 2, y: -b.h / 2, width: b.w, height: b.h }, g);
      if (st) el("circle", { "class": "dot", cx: b.w / 2 + 2, cy: -b.h / 2 - 2, r: 6 }, g);
      el("text", { "class": "name", y: b.h / 2 + 20, "text-anchor": "middle" }, g).textContent = name;
      haRole(g, n, b.w / 2 + 8, -b.h / 2 + 10, "start");
    } else {
      if (st === "down") el("circle", { "class": "pulse", r: 34 }, g);
      el("rect", { "class": "ring", x: -30, y: -30, width: 60, height: 60, rx: 18 }, g);
      el("rect", { "class": "body", x: -24, y: -24, width: 48, height: 48, rx: 14 }, g);
      var kind = n.kind === "submap" ? "submap" : ((live.devices[n.device] || deviceMeta(n.device) || {}).kind || "other");
      el("path", { "class": "glyph", d: icons[kind] || icons.other, transform: "translate(-12 -12)" }, g);
      el("text", { "class": "name", y: 44, "text-anchor": "middle" }, g).textContent = name;
    }
    var d = n.kind === "device" ? live.devices[n.device] || deviceMeta(n.device) : null;
    if (d && !rackOf(n)) {
      el("text", { "class": "addr", y: (p ? b.h / 2 + 35 : 60), "text-anchor": "middle" }, g).textContent = d.address + (d.down ? "  · " + d.down + " " + T("down") : "");
    }
    el("title", {}, g).textContent = name + (st ? " — " + T(cap(st)) : "");
  }

  function drawLink(k) {
    var a = nodeByID(k.a), b = nodeByID(k.b);
    if (!a || !b) return;
    var s = linkState(k), pts = routePoints(k);
    var cls = "link k-" + (k.kind || "ethernet") + (s.down ? " down" : "") + (selected && selected.type === "link" && selected.id === k.id ? " sel" : "");
    if (!s.down && s.known) cls += s.util > 0.8 ? " u3" : s.util > 0.5 ? " u2" : "";
    cls += onPath("links", k.id);
    if (vlan && vlanMatch(linkVLANs(k, s))) cls += " invlan";
    if (vlanMismatch(k, s)) cls += " vlanwarn";
    var g = el("g", { "class": cls, "data-link": k.id }, gLinks);
    var gl = gLabels === gLinks ? g : el("g", { "class": cls + " labels" }, gLabels);
    var d = pathD(pts), w = 2.2 + Math.min(7, s.util * 9);
    if (k.kind === "fiber" || k.kind === "fiber-sm") w += 0.6;
    el("path", { "class": "hit", d: d }, g);
    if (k.kind === "dac") el("path", { "class": "casing", d: d, "stroke-width": (w + 2.5).toFixed(1) }, g);
    el("path", { "class": "wire", d: d, "stroke-width": w.toFixed(1) }, g);
    var len = lengthOf(pts);
    // port names exactly where the cable ends: on the port of the picture, or
    // where it leaves the picture; traffic in the middle (cables are drawn
    // over the pictures, like patch cords in front of a rack)
    var offA = anchor(a, slotOf(k, "a")).port ? 0 : Math.min(len * 0.4, edgeDist(a, pts[0], pts[1]));
    var offB = anchor(b, slotOf(k, "b")).port ? 0 : Math.min(len * 0.4, edgeDist(b, pts[pts.length - 1], pts[pts.length - 2]));
    var nameA = s.a ? s.a.name : k.a_slot ? slotName(a, k.a_slot) : "";
    var nameB = s.b ? s.b.name : k.b_slot ? slotName(b, k.b_slot) : "";
    if (len < 70) nameA = nameB = ""; // a short patch cord: the panel names its ports
    if (nameA) { var pa = pointAlong(pts, offA); label(gl, "port", pa.x, pa.y, nameA); }
    if (nameB) { var pb = pointAlong(pts, len - offB); label(gl, "port", pb.x, pb.y, nameB); }
    var text = k.label || "";
    if (vlan) {
      var pv = findVLAN(linkVLANs(k, s), vlan);
      text = "VLAN " + vlan + (pv ? " · " + (pv.tagged ? T("tagged") : T("untagged")) : "");
    } else if (s.ab !== null && s.ab !== undefined && len > 190) text = (text ? text + "  " : "") + "→ " + bits(s.ab) + "  ← " + bits(s.ba);
    var mid = pointAlong(pts, len / 2);
    if (text) label(gl, "traffic", mid.x, mid.y, text);
    // the VLAN mode (access / trunk, native VLAN) and the VXLAN VNI
    var vt = vlan ? "" : modeText(linkMode(k, s), 30);
    if (k.vni) vt = (vt ? vt + " · " : "") + "VNI " + k.vni;
    if (vt && len > 120) label(gl, "vlan", mid.x, mid.y + (text ? 13 : 0), vt);
  }
  // VXLAN tunnels the devices report, between devices on the map (a cable
  // of kind VXLAN with the same VNI between them stands for it)
  function drawTunnels() {
    (live.tunnels || []).forEach(function (t) {
      var a = nodeOfDevice(t.a), b = nodeOfDevice(t.b);
      if (!a || !b) return;
      var drawn = layout.links.some(function (k) {
        var ka = nodeByID(k.a), kb = nodeByID(k.b);
        return k.kind === "vxlan" && (!k.vni || k.vni === t.vni) && ka && kb && ((ka.device === t.a && kb.device === t.b) || (ka.device === t.b && kb.device === t.a));
      });
      if (drawn) return;
      // an arc to the side, so a tunnel stays apart from the cables (and
      // visible between devices in the same rack)
      var pa = anchor(a), pb = anchor(b), dx = pb.x - pa.x, dy = pb.y - pa.y, d = Math.max(1, Math.hypot(dx, dy)), off = 40 + d * 0.15;
      var c = { x: (pa.x + pb.x) / 2 - dy / d * off, y: (pa.y + pb.y) / 2 + dx / d * off };
      if (rackOf(a) && a.parent === b.parent) { // the same rack: out of its right side
        var right = Math.max(a.x + box(a).w / 2, b.x + box(b).w / 2);
        pa = { x: a.x + box(a).w / 2, y: a.y };
        pb = { x: b.x + box(b).w / 2, y: b.y };
        c = { x: right + 150 + d * 0.3, y: (pa.y + pb.y) / 2 };
      }
      var g = el("g", { "class": "tunnel" }, gLinks);
      el("path", { "class": "wire", d: "M" + pa.x + " " + pa.y + "Q" + c.x + " " + c.y + " " + pb.x + " " + pb.y }, g);
      el("title", {}, g).textContent = "VXLAN VNI " + t.vni + (t.name ? " (" + t.name + ")" : "") + " — " + nodeName(a) + " ↔ " + nodeName(b);
      label(g, "vlan", (pa.x + 2 * c.x + pb.x) / 4, (pa.y + 2 * c.y + pb.y) / 4, "VXLAN " + t.vni);
    });
  }
  // drawHostLinks: a dashed line from each hypervisor box to the physical
  // server it runs on, edge to edge (not a cable; paths go along it)
  function drawHostLinks() {
    layout.nodes.forEach(function (h) {
      if (h.kind !== "host") return;
      hostServers(h).forEach(function (srv) {
        var a = edgePoint(h, srv), b = edgePoint(srv, h);
        var g = el("g", { "class": "hostlink" + (path && path.hostlinks && path.hostlinks[h.id + ":" + srv.id] ? " onpath" : "") }, gLinks);
        el("path", { "class": "wire", d: "M" + a.x + " " + a.y + "L" + b.x + " " + b.y }, g);
        el("title", {}, g).textContent = nodeName(h) + " — " + T("runs on") + " " + nodeName(srv);
      });
    });
  }
  // edgePoint: where the line from n towards o leaves the box of n
  function edgePoint(n, o) {
    var b = box(n), dx = o.x - n.x, dy = o.y - n.y;
    if (!dx && !dy) return { x: n.x, y: n.y };
    var t = Math.min(dx ? (b.w / 2 + 4) / Math.abs(dx) : Infinity, dy ? (b.h / 2 + 4) / Math.abs(dy) : Infinity, 1);
    return { x: n.x + dx * t, y: n.y + dy * t };
  }
  function nodeOfDevice(id) {
    for (var i = 0; i < layout.nodes.length; i++) if (layout.nodes[i].kind === "device" && layout.nodes[i].device === id) return layout.nodes[i];
    return null;
  }
  // edgeDist: how far along the segment p0→p1 (p0 the centre of n) the
  // cable leaves the picture of n
  function edgeDist(n, p0, p1) {
    var b = box(n), dx = p1.x - p0.x, dy = p1.y - p0.y, l = Math.hypot(dx, dy);
    if (!l) return 0;
    var t = Math.min(dx ? b.w / 2 / Math.abs(dx) : Infinity, dy ? b.h / 2 / Math.abs(dy) : Infinity);
    return Math.min(l, t * l);
  }
  function label(g, cls, x, y, text) {
    el("text", { "class": "lbl " + cls, x: x, y: cls === "port" ? y : y + 4, "text-anchor": "middle", "dominant-baseline": cls === "port" ? "central" : "auto" }, g).textContent = text;
  }
  // bend handles of the selected cable (editing)
  function drawHandles() {
    if (!editing || !selected || selected.type !== "link") return;
    var k = linkByID(selected.id);
    if (!k) return;
    var a = nodeByID(k.a), b = nodeByID(k.b);
    var raw = [anchor(a, slotOf(k, "a"))].concat((k.points || []).map(function (p) { return { x: p[0], y: p[1] }; })).concat([anchor(b, slotOf(k, "b"))]);
    for (var i = 0; i < raw.length - 1; i++) {
      var m = { x: (raw[i].x + raw[i + 1].x) / 2, y: (raw[i].y + raw[i + 1].y) / 2 };
      var add = el("g", { "class": "addh", "data-add": i, transform: "translate(" + m.x + " " + m.y + ")" }, gHandles);
      el("circle", { r: 7 }, add);
      el("path", { d: "M-3.5 0h7M0 -3.5v7" }, add);
    }
    (k.points || []).forEach(function (p, j) { el("circle", { "class": "handle", cx: p[0], cy: p[1], r: 6, "data-handle": j }, gHandles); });
  }

  // ---- side panel
  function closePanel() { panel.hidden = true; panel.innerHTML = ""; }
  function openPanel(title, status) {
    panel.innerHTML = "";
    var head = h("div", { "class": "mp-head" });
    head.appendChild(h("b", {}, title));
    if (status) { var b = h("span", { "class": "badge " + badgeClass(status) }); b.appendChild(h("i")); b.appendChild(document.createTextNode(T(cap(status)))); head.appendChild(b); }
    var x = h("button", { type: "button", "class": "iconbtn mp-close", title: T("Close"), "aria-label": T("Close") }, "×");
    x.addEventListener("click", function () { selected = null; closePanel(); render(); });
    head.appendChild(x);
    panel.appendChild(head);
    panel.hidden = false;
    return panel;
  }
  function badgeClass(s) { return { up: "ok", down: "bad", warning: "warn", paused: "muted", unknown: "muted" }[s] || "muted"; }
  function row(dl, k, v) { dl.appendChild(h("dt", {}, k)); var dd = h("dd"); if (typeof v === "string") dd.textContent = v; else dd.appendChild(v); dl.appendChild(dd); }
  // fromMap marks links from the map, so the page offers a way back
  function fromMap() { return kioskBase ? "" : "?map=" + mapID; }
  function btn(text, href, cls) { return h("a", { "class": "btn " + (cls || "secondary") + " sm", href: href }, text); }
  function button(text, cls, fn) { var b = h("button", { type: "button", "class": "btn " + (cls || "secondary") + " sm" }, text); b.addEventListener("click", fn); return b; }
  function field(p, text, input) { var l = h("label", {}, text); l.appendChild(input); p.appendChild(l); return input; }
  function select(options, value, fn) {
    var s = h("select");
    options.forEach(function (o) { var op = h("option", { value: o[0] }, o[1]); if (String(o[0]) === String(value)) op.selected = true; s.appendChild(op); });
    s.addEventListener("change", function () { fn(s.value); });
    return s;
  }
  function range(min, max, step, value, fn) {
    var r = h("input", { type: "range", min: min, max: max, step: step });
    r.value = value;
    r.addEventListener("input", function () { fn(parseFloat(r.value)); });
    return r;
  }

  function showNode(n) {
    var st = nodeStatus(n);
    if (n.kind === "label") {
      if (!editing) { closePanel(); return; }
      var p0 = openPanel(T("Text"));
      var inp = h("input", { value: n.label, maxlength: "200", "aria-label": T("Text") });
      inp.addEventListener("input", function () { n.label = inp.value; render(); changed(); });
      p0.appendChild(inp);
      return;
    }
    if (n.kind === "rack") { showRack(n); return; }
    if (n.kind === "host") { showHost(n); return; }
    var p = openPanel(nodeName(n) || T("Item"), st);
    var dl = h("dl", { "class": "facts" });
    if (n.kind === "device") {
      var d = live.devices[n.device] || deviceMeta(n.device) || {};
      row(dl, T("Address"), d.address || "");
      row(dl, T("Kind"), T("kind:" + (d.kind || "other")));
      if (d.vendor) row(dl, T("Vendor"), d.vendor);
      if (d.checks !== undefined) row(dl, T("Checks"), d.checks + (d.down ? " · " + d.down + " " + T("down") : ""));
      p.appendChild(dl);
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
        p.appendChild(h("h3", {}, T("Ports")));
        p.appendChild(list);
      }
      if (!kioskBase) {
        var acts = h("div", { "class": "row mt" });
        acts.appendChild(btn(T("Metrics"), "/devices/" + n.device + fromMap(), "primary"));
        if (d.web) { var wa = btn(d.web_name || T("Open web interface"), d.web); wa.target = "_blank"; wa.rel = "noopener noreferrer"; acts.appendChild(wa); }
        (d.links || []).forEach(function (l) { var la = btn(l.name, l.url); la.target = "_blank"; la.rel = "noopener noreferrer"; acts.appendChild(la); });
        (d.tools || []).forEach(function (t) { acts.appendChild(btn(t.name, "/devices/" + n.device + "/open/" + t.id)); });
        p.appendChild(acts);
        if (d.tools && d.tools.length) {
          // the tools need the Launcher paired with this console
          var lp = h("p", { "class": "muted small" }, T("SSH, Remote Desktop and the other tools open through the WatchZit Launcher.") + " ");
          lp.appendChild(h("a", { href: "/launcher" + fromMap() }, T("Set it up")));
          p.appendChild(lp);
        }
      }
    } else if (n.kind === "submap" && !kioskBase) {
      var acts2 = h("div", { "class": "row mt" });
      acts2.appendChild(btn(T("Open map"), "/maps/" + n.submap, "primary"));
      p.appendChild(acts2);
    }
    if (!editing) return;
    // an item becomes a monitored device (picture, place and cables kept),
    // and a device can go back to being a plain item
    if (n.kind === "item" || n.kind === "device") {
      var on = {};
      layout.nodes.forEach(function (x) { if (x.kind === "device" && x !== n) on[x.device] = true; });
      var devs = (meta.devices || []).filter(function (d) { return !on[d.id]; }).sort(function (a, b) { return a.name.localeCompare(b.name); });
      field(p, T("Monitored device"), select([["0", T("None (picture only)")]].concat(devs.map(function (d) { return [String(d.id), d.name + " · " + d.address]; })), n.kind === "device" ? n.device : 0, function (v) {
        var id = +v;
        if (id) { n.kind = "device"; n.device = id; }
        else { n.kind = "item"; delete n.device; }
        render(); changed(); showSelection();
      }));
    }
    var lab = field(p, T("Label"), h("input", { value: n.label || "", maxlength: "200", placeholder: nodeName(n) }));
    lab.addEventListener("input", function () { n.label = lab.value; render(); changed(); });
    // picture
    p.appendChild(h("h3", {}, T("Picture")));
    var pic = picOf(n), prow = h("div", { "class": "mp-pic" });
    if (pic) prow.appendChild(h("img", { src: picURL(pic), alt: "" }));
    prow.appendChild(button(T("Change picture"), "secondary", function () {
      var id = n.id;
      pickPicture(function (icon) { var x = nodeByID(id); x.icon = icon; x.size = 0; fixMount(x); selected = { type: "node", id: id }; render(); changed(); showSelection(); }, n.kind !== "item", pictureVendor(n));
    }));
    p.appendChild(prow);
    if (!n.parent) field(p, T("Size"), range(20, 600, 2, box(n).w, function (v) { n.size = v; render(); changed(); }));
    if (n.kind === "device" || n.kind === "item") haField(p, n);
    // the hypervisor host it runs on (virtual machines and devices)
    var hosts = layout.nodes.filter(function (x) { return x.kind === "host"; });
    if (hosts.length && (n.kind === "device" || n.kind === "item")) {
      var hopts = [["0", T("None")]].concat(hosts.map(function (x) { return [String(x.id), nodeName(x)]; }));
      field(p, T("Runs on hypervisor"), select(hopts, hostOf(n) ? n.parent : 0, function (v) {
        var id = +v, was = hostOf(n);
        if (!id) { n.parent = 0; n.u = 0; if (was) { var wb = box(was); n.x = was.x + wb.w / 2 + 80; } }
        else { n.parent = id; n.u = guestsOf(nodeByID(id)).length + 1; }
        render(); changed(); showSelection();
      }));
    }
    // rack mounting
    var racks = layout.nodes.filter(function (r) { return r.kind === "rack"; });
    if (racks.length && !hostOf(n)) {
      p.appendChild(h("h3", {}, T("Rack")));
      var opts = [["0", T("Not in a rack")]].concat(racks.map(function (r) { return [String(r.id), (r.label || T("Rack")) + " · " + r.u + "U"]; }));
      field(p, T("Rack"), select(opts, n.parent || 0, function (v) {
        var id = +v;
        if (!id) { n.parent = 0; n.u = 0; n.x += RACK_W + 60; }
        else { n.parent = id; n.u = freeUnit(nodeByID(id), unitsOf(n), n) || 1; }
        render(); changed(); showSelection();
      }));
      if (n.parent) {
        var r0 = nodeByID(n.parent);
        var us = h("input", { type: "number", min: "1", max: String(r0.u - unitsOf(n) + 1), value: String(n.u) });
        us.addEventListener("change", function () { n.u = Math.max(1, Math.min(r0.u - unitsOf(n) + 1, +us.value || 1)); render(); changed(); });
        field(p, T("Mounted at unit"), us);
      }
    }
  }
  function fixMount(n) {
    if (!rackOf(n)) return;
    var r = nodeByID(n.parent);
    if (r && n.u + unitsOf(n) - 1 > r.u) n.u = Math.max(1, r.u - unitsOf(n) + 1);
  }
  // freeUnit finds the highest free position for hgt units in a rack
  function freeUnit(r, hgt, self) {
    for (var u = r.u - hgt + 1; u >= 1; u--) if (unitFree(r, u, hgt, self)) return u;
    return 0;
  }
  function unitFree(r, u, hgt, self) {
    for (var i = 0; i < layout.nodes.length; i++) {
      var c = layout.nodes[i];
      if (c.parent !== r.id || c === self) continue;
      if (c.u <= u + hgt - 1 && u <= c.u + unitsOf(c) - 1) return false;
    }
    return true;
  }

  var rackSizes = [4, 6, 9, 12, 15, 18, 22, 24, 27, 32, 36, 42, 45, 47, 48];
  function showRack(n) {
    var p = openPanel((n.label || T("Rack")) + " · " + n.u + "U", nodeStatus(n));
    var inside = layout.nodes.filter(function (c) { return c.parent === n.id; }).sort(function (a, b) { return b.u - a.u; });
    var list = h("ul", { "class": "mp-ports" });
    inside.forEach(function (c) {
      var st = nodeStatus(c);
      var li = h("li", { "class": st === "down" ? "down" : st === "up" ? "up" : "" });
      li.appendChild(h("b", {}, nodeName(c) || "—"));
      li.appendChild(h("span", {}, "U" + c.u + (unitsOf(c) > 1 ? "–" + (c.u + unitsOf(c) - 1) : "")));
      list.appendChild(li);
    });
    p.appendChild(h("h3", {}, T("Devices in rack")));
    p.appendChild(inside.length ? list : h("p", { "class": "muted small" }, "—"));
    if (!editing) return;
    var nm = field(p, T("Rack name"), h("input", { value: n.label || "", maxlength: "200" }));
    nm.addEventListener("input", function () { n.label = nm.value; render(); changed(); });
    var used = 0;
    inside.forEach(function (c) { used = Math.max(used, c.u + unitsOf(c) - 1); });
    field(p, T("Units"), select(rackSizes.filter(function (u) { return u >= used; }).map(function (u) { return [u, u + "U"]; }), n.u, function (v) { n.u = +v; render(); changed(); }));
    p.appendChild(h("p", { "class": "muted small" }, T("Drag devices into the rack; they snap to the units.")));
  }

  // ---- hypervisor hosts
  function hvList() {
    return libList.filter(function (i) { return i.type === "hypervisor"; }).map(function (i) { return ["lib:" + i.name, i.label]; });
  }
  function hostFields(p, n) {
    field(p, T("Hypervisor"), select(hvList(), n.icon || "lib:hv-esxi", function (v) { n.icon = v; render(); changed(); }));
    var ds = ((meta && meta.devices) || []).slice().sort(function (a, b) { return a.name.localeCompare(b.name); });
    field(p, T("Physical server"), select([["0", T("Choose the server")]].concat(ds.map(function (d) { return [String(d.id), d.name + " · " + d.address]; })), n.host_device || 0, function (v) {
      n.host_device = +v || 0; render(); changed();
    }));
  }
  function showHost(n) {
    var p = openPanel(nodeName(n), nodeStatus(n));
    var hp = picOf(n), dl = h("dl", { "class": "facts" });
    if (hp) row(dl, T("Hypervisor"), hp.label);
    var hd = n.host_device && (live.devices[n.host_device] || deviceMeta(n.host_device));
    if (hd) row(dl, T("Physical server"), hd.name + " · " + hd.address);
    p.appendChild(dl);
    var inside = guestsOf(n), list = h("ul", { "class": "mp-ports" });
    inside.forEach(function (c) {
      var st = nodeStatus(c);
      var li = h("li", { "class": st === "down" ? "down" : st === "up" ? "up" : "" });
      li.appendChild(h("b", {}, nodeName(c) || "—"));
      var cp = picOf(c);
      li.appendChild(h("span", {}, cp && cp.label ? cp.label.replace(/ \(VM\)$/, "") : ""));
      list.appendChild(li);
    });
    p.appendChild(h("h3", {}, T("Virtual machines")));
    p.appendChild(inside.length ? list : h("p", { "class": "muted small" }, "—"));
    if (hd && !kioskBase) {
      var acts = h("div", { "class": "row mt" });
      acts.appendChild(btn(T("Metrics") + " · " + hd.name, "/devices/" + n.host_device + fromMap(), "secondary"));
      p.appendChild(acts);
    }
    if (!editing) return;
    var nm = field(p, T("Name"), h("input", { value: n.label || "", maxlength: "200", placeholder: nodeName(n) }));
    nm.addEventListener("input", function () { n.label = nm.value; render(); changed(); });
    hostFields(p, n);
    clusterFields(p, n);
    haField(p, n);
    hostFields(p, n);
    p.appendChild(h("p", { "class": "muted small" }, T("Drag virtual machines and devices into the box; drag them within it to change the order. Add a VM as a device of the console to see its state, or as an item with a picture of its system.")));
  }
  function addHost() {
    var p = openPanel(T("Add hypervisor"));
    var n = { kind: "host", label: "", icon: "lib:hv-esxi", host_device: 0 };
    var nm = field(p, T("Name"), h("input", { maxlength: "200", placeholder: "esx01" }));
    hostFields(p, n);
    var err = h("p", { "class": "small warntext", hidden: "" }, T("Choose the physical server the hypervisor runs on."));
    p.appendChild(err);
    p.appendChild(button(T("Add"), "primary", function () {
      if (!n.host_device) { err.hidden = false; return; }
      n.label = nm.value; addNode(n);
    }));
  }
  // hostServers: the nodes of the physical servers a hypervisor (cluster)
  // runs on, those that are on the map
  function hostServers(h) {
    var ids = [h.host_device].concat(h.host_devices || []), out = [];
    layout.nodes.forEach(function (o) { if (o.kind === "device" && ids.indexOf(o.device) >= 0) out.push(o); });
    return out;
  }
  // clusterFields: more physical servers of a hypervisor cluster
  function clusterFields(p, n) {
    var ds = (meta && meta.devices) || [], wrap = h("div", { "class": "stack" });
    p.appendChild(h("h3", {}, T("Cluster")));
    p.appendChild(wrap);
    function fill() {
      wrap.innerHTML = "";
      (n.host_devices || []).forEach(function (id) {
        var d = deviceMeta(id), li = h("div", { "class": "row" });
        li.appendChild(h("span", {}, d ? d.name + " · " + d.address : "#" + id));
        li.appendChild(button("×", "ghost", function () { n.host_devices = n.host_devices.filter(function (x) { return x !== id; }); render(); changed(); fill(); }));
        wrap.appendChild(li);
      });
      var taken = [n.host_device].concat(n.host_devices || []);
      var free = ds.filter(function (d) { return taken.indexOf(d.id) < 0; }).sort(function (a, b) { return a.name.localeCompare(b.name); });
      if (free.length) wrap.appendChild(select([["0", T("Add a server of the cluster")]].concat(free.map(function (d) { return [String(d.id), d.name + " · " + d.address]; })), 0, function (v) {
        if (+v) { n.host_devices = (n.host_devices || []).concat([+v]); render(); changed(); fill(); }
      }));
    }
    fill();
  }

  // ---- HA groups: devices that stand in for each other
  var HA_KINDS = [["firewall", "Firewall HA"], ["router", "Routers (VRRP / HSRP)"], ["switch", "Switch stack / MLAG / vPC"], ["server", "Server cluster"], ["storage", "Storage HA"], ["other", "Other"]];
  var nextHA = -1;
  function haOf(n) {
    for (var i = 0; i < layout.ha.length; i++) if (layout.ha[i].nodes.indexOf(n.id) >= 0) return layout.ha[i];
    return null;
  }
  function haByID(id) { for (var i = 0; i < layout.ha.length; i++) if (layout.ha[i].id === id) return layout.ha[i]; return null; }
  function haMembers(g) { return g.nodes.map(nodeByID).filter(Boolean); }
  function haName(g) { return g.name || haMembers(g).map(nodeName).join(" / ") || T("HA group"); }
  // haStatus: all members up — up; some down — warning (redundancy lost);
  // all down — down
  function haStatus(g) {
    var up = 0, down = 0;
    haMembers(g).forEach(function (n) {
      var st = nodeStatus(n);
      if (st === "down") down++; else if (st === "up" || st === "warning") up++;
    });
    if (down && !up) return "down";
    if (down) return "warning";
    return up ? "up" : "";
  }
  function haKindName(k) { for (var i = 0; i < HA_KINDS.length; i++) if (HA_KINDS[i][0] === k) return T(HA_KINDS[i][1]); return k; }
  // drawHA: a dashed frame around the members, its name on top
  function drawHA(g) {
    var ms = haMembers(g);
    if (!ms.length) return;
    var x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    ms.forEach(function (n) {
      var b = box(n);
      x0 = Math.min(x0, n.x - b.w / 2); y0 = Math.min(y0, n.y - b.h / 2);
      x1 = Math.max(x1, n.x + b.w / 2); y1 = Math.max(y1, n.y + b.h / 2 + (rackOf(n) ? 0 : 26));
    });
    var st = haStatus(g), sel = selected && selected.type === "ha" && selected.id === g.id;
    var on = path && ms.some(function (n) { return path.nodes[n.id]; });
    var gr = el("g", { "class": "hagroup" + (st ? " s-" + st : "") + (sel ? " sel" : "") + (on ? " onpath" : ""), "data-ha": g.id }, gRacks);
    el("rect", { "class": "haframe", x: x0 - 14, y: y0 - 26, width: x1 - x0 + 28, height: y1 - y0 + 40, rx: 12 }, gr);
    el("text", { "class": "haname", x: x0 - 6, y: y0 - 10 }, gr).textContent = "HA · " + haName(g) + (st === "warning" ? "  — " + T("redundancy lost") : "");
    el("title", {}, gr).textContent = haKindName(g.kind) + ": " + haName(g);
  }
  function showHA(g) {
    var p = openPanel("HA · " + haName(g), haStatus(g));
    var dl = h("dl", { "class": "facts" });
    row(dl, T("Kind"), haKindName(g.kind));
    var st = haStatus(g);
    row(dl, T("Redundancy"), st === "warning" ? T("lost: some members are down") : st === "down" ? T("all members are down") : st === "up" ? T("all members are up") : "—");
    p.appendChild(dl);
    var list = h("ul", { "class": "mp-ports" });
    haMembers(g).forEach(function (n) {
      var s2 = nodeStatus(n), li = h("li", { "class": s2 === "down" ? "down" : s2 === "up" ? "up" : "" });
      li.appendChild(h("b", {}, nodeName(n) || "—"));
      var hd2 = n.kind === "device" ? live.devices[n.device] : null;
      li.appendChild(h("span", {}, T(cap(s2 || "unknown")) + (hd2 && hd2.ha ? " · " + (hd2.ha === "master" ? T("active") : T("standby")) + (hd2.ha_since ? " " + T("since") + " " + new Date(hd2.ha_since).toLocaleString() : "") : "")));
      list.appendChild(li);
    });
    p.appendChild(h("h3", {}, T("Members")));
    p.appendChild(list);
    if (g.nodes.length < 2) p.appendChild(h("p", { "class": "small warntext" }, T("Add a second member: open another device and choose this group under HA group.")));
    if (!editing) return;
    var nm = field(p, T("Name"), h("input", { value: g.name || "", maxlength: "100", placeholder: "fw-ha" }));
    nm.addEventListener("input", function () { g.name = nm.value; render(); changed(); });
    field(p, T("Kind"), select(HA_KINDS.map(function (k) { return [k[0], T(k[1])]; }), g.kind, function (v) { g.kind = v; render(); changed(); }));
    p.appendChild(h("p", { "class": "muted small" }, T("Paths go through every member of the group. Mark the cable between the members (heartbeat, sync, stack, peer link) as an HA link; it is not a way for traffic.")));
    p.appendChild(button(T("Delete group"), "ghost", function () { layout.ha = layout.ha.filter(function (x) { return x !== g; }); selected = null; closePanel(); render(); changed(); }));
  }
  // haField: the HA group of a node, in its panel
  function haField(p, n) {
    var g = haOf(n);
    var opts = [["0", T("None")]].concat(layout.ha.map(function (x) { return [String(x.id), haName(x) + " (" + x.nodes.length + ")"]; })).concat([["new", T("New HA group")]]);
    field(p, T("HA group"), select(opts, g ? g.id : 0, function (v) {
      if (g) g.nodes = g.nodes.filter(function (id) { return id !== n.id; });
      layout.ha = layout.ha.filter(function (x) { return x.nodes.length; });
      if (v === "new") {
        var kind = { firewall: "firewall", router: "router", "switch": "switch", server: "server" }[(live.devices[n.device] || deviceMeta(n.device) || {}).kind] || (n.kind === "host" ? "server" : "other");
        var ng = { id: nextHA--, name: "", kind: kind, nodes: [n.id] };
        layout.ha.push(ng);
        selected = { type: "ha", id: ng.id };
      } else if (+v) {
        var t = haByID(+v);
        if (t) t.nodes.push(n.id);
      }
      render(); changed(); showSelection();
    }));
  }

  // usedPorts: the interfaces and picture ports of a node that other cables
  // than k already use, with the node at their other end
  function usedPorts(n, k) {
    var res = { ifs: {}, slots: {} };
    layout.links.forEach(function (l) {
      if (l === k) return;
      [["a", "b"], ["b", "a"]].forEach(function (s) {
        if (l[s[0]] !== n.id) return;
        var other = nodeName(nodeByID(l[s[1]])) || T("Item");
        if (l[s[0] + "_if"]) res.ifs[l[s[0] + "_if"]] = other;
        var sl = slotOf(l, s[0]);
        if (sl) res.slots[sl] = other;
      });
    });
    return res;
  }
  // markUsed disables the options in use and says where their cable goes
  function markUsed(s, used, value) {
    Array.prototype.forEach.call(s.options, function (o) {
      if (o.value !== "0" && used[o.value] !== undefined && String(value) !== o.value) {
        o.disabled = true;
        o.textContent += " — " + T("connected to") + " " + used[o.value];
      }
    });
  }
  function interfaceSelect(n, value, k, onChange) {
    var ps = n && n.kind === "device" ? live.ports[n.device] || {} : {};
    var opts = [["0", T("Not set")]];
    Object.keys(ps).sort(function (x, y) { return x - y; }).forEach(function (idx) { opts.push([idx, ps[idx].name + (ps[idx].alias ? " · " + ps[idx].alias : "")]); });
    var s = select(opts, value || 0, function (v) { onChange(+v, ps[v] ? ps[v].name : ""); });
    markUsed(s, usedPorts(n, k).ifs, value || 0);
    s.disabled = !editing;
    return s;
  }
  function slotSelect(n, value, k, onChange) {
    var p = box(n).pic, opts = [["0", T("Automatic")]];
    for (var i = 1; p && i <= p.ports.length; i++) opts.push([i, T("Port") + " " + slotName(n, i)]);
    var s = select(opts, value || 0, function (v) { onChange(+v); });
    markUsed(s, usedPorts(n, k).slots, value || 0);
    s.disabled = !editing;
    return s;
  }
  function showLink(k) {
    var a = nodeByID(k.a), b = nodeByID(k.b), s = linkState(k);
    var p = openPanel(T("cable:" + (k.kind || "ethernet")), s.down ? "down" : s.known ? "up" : "");
    [[a, "a"], [b, "b"]].forEach(function (end) {
      var n = end[0], side = end[1];
      p.appendChild(h("h3", {}, nodeName(n) || T("Item")));
      var dl = h("dl", { "class": "facts" });
      if (n.kind === "device") row(dl, T("Interface"), interfaceSelect(n, k[side + "_if"], k, function (v, name) {
        k[side + "_if"] = v;
        if (!k[side + "_slot"]) k[side + "_slot"] = guessSlot(n, name);
        render(); changed(); showSelection();
      }));
      if (box(n).pic && box(n).pic.ports && box(n).pic.ports.length) row(dl, T("Port on picture"), slotSelect(n, k[side + "_slot"], k, function (v) { k[side + "_slot"] = v; render(); changed(); }));
      p.appendChild(dl);
    });
    var dl2 = h("dl", { "class": "facts" });
    if (s.ab !== null && s.ab !== undefined) {
      row(dl2, nodeName(a) + " → " + nodeName(b), bits(s.ab));
      row(dl2, nodeName(b) + " → " + nodeName(a), bits(s.ba));
    }
    if (s.speed) row(dl2, T("Speed"), bits(s.speed));
    var lm = linkMode(k, s);
    if (lm) row(dl2, T("VLAN mode"), modeText(lm) + (k.vlans && k.vlans.length ? "" : " · " + T("detected")));
    var vl = vlanText(linkVLANs(k, s));
    if (vl) row(dl2, T("VLANs"), vl);
    if (k.vni) row(dl2, "VXLAN VNI", String(k.vni));
    var warn = vlanMismatch(k, s);
    if (warn) row(dl2, T("Warning"), h("span", { "class": "warntext" }, warn));
    p.appendChild(dl2);
    if (editing) {
      field(p, T("Cable"), select(["ethernet", "fiber", "fiber-sm", "dac", "wireless", "vpn", "vxlan", "ha", "wan", "serial", "power"].map(function (x) { return [x, T("cable:" + x)]; }), k.kind || "ethernet", function (v) { k.kind = v; render(); changed(); showSelection(); }));
      field(p, T("Route"), select([["straight", T("Straight")], ["ortho", T("90° bends")], ["diagonal", T("45° bends")], ["curve", T("Curved")]], k.route || "straight", function (v) { k.route = v; render(); changed(); }));
      p.appendChild(h("p", { "class": "muted small" }, T("Drag the + points to bend the cable; drag the round points to move a bend; double-click a point to remove it.")));
      var ll = field(p, T("Label"), h("input", { value: k.label || "", maxlength: "100" }));
      ll.addEventListener("input", function () { k.label = ll.value; render(); changed(); });
      vlanFields(p, k, s);
    }
    if (!kioskBase) {
      var acts = h("div", { "class": "row mt" });
      // the port graphs of each end on its device page
      if (s.a && s.a.check) acts.appendChild(btn(T("Graphs") + " · " + nodeName(a), "/devices/" + a.device + fromMap() + "#graph-" + s.a.check, "secondary"));
      if (s.b && s.b.check) acts.appendChild(btn(T("Graphs") + " · " + nodeName(b), "/devices/" + b.device + fromMap() + "#graph-" + s.b.check, "secondary"));
      p.appendChild(acts);
    }
  }

  function showSelection() {
    if (!selected) { closePanel(); return; }
    if (selected.type === "node") { var n = nodeByID(selected.id); if (n) showNode(n); else closePanel(); }
    else if (selected.type === "ha") { var g = haByID(selected.id); if (g) showHA(g); else closePanel(); }
    else { var k = linkByID(selected.id); if (k) showLink(k); else closePanel(); }
  }

  // ---- pictures: built-in library and uploads
  // picture types: device kinds, storage, and the categories of the rest
  var picTypes = [["", "All types"], ["switch", "Switch"], ["router", "Router"], ["firewall", "Firewall"], ["server", "Server"], ["storage", "Storage / NAS"], ["hypervisor", "Hypervisor"], ["vm", "Virtual machine"],
    ["accesspoint", "Access point"], ["ups", "UPS"], ["power", "Power"], ["camera", "Camera"], ["pc", "Computer"], ["printer", "Printer"],
    ["rack", "Rack"], ["logical", "Logical"], ["other", "Other"], ["uploaded", "Uploaded"]];
  function picType(i) {
    if (i.type) return i.type;
    if (i.kind && i.kind !== "other") return i.kind;
    return ["rack", "logical", "power", "uploaded"].indexOf(i.cat) >= 0 ? i.cat : "other";
  }
  // pickPicture shows the pictures; vendor preselects a manufacturer
  function pickPicture(onPick, allowNone, vendor) {
    var p = openPanel(T("Picture"));
    var q = h("input", { type: "search", placeholder: T("Search, e.g. DL380 or CRS326"), "aria-label": T("Search") });
    var grid = h("div", { "class": "mp-grid" });
    // vendor: "" = the generic pictures, "*" = all; a search looks through all
    var vendors = [];
    libList.forEach(function (i) { if (i.vendor && vendors.indexOf(i.vendor) < 0) vendors.push(i.vendor); });
    Object.keys(meta.assets || {}).forEach(function (id) { var m = meta.assets[id].meta || {}; if (m.vendor && vendors.indexOf(m.vendor) < 0) vendors.push(m.vendor); });
    vendors.sort();
    var vend = select([["", T("Generic pictures")], ["*", T("All manufacturers")]].concat(vendors.map(function (v) { return [v, v]; })), vendor || "", function () { fill(); });
    vend.setAttribute("aria-label", T("Manufacturer"));
    var kind = select(picTypes.map(function (t) { return [t[0], T(t[1])]; }), "", function () { fill(); });
    kind.setAttribute("aria-label", T("Type"));
    function fill() {
      grid.innerHTML = "";
      var f = q.value.toLowerCase().trim();
      var items = libList.filter(function (i) { return f || vend.value === "*" || (i.vendor || "") === vend.value; })
        .map(function (i) { return { icon: "lib:" + i.name, src: "/static/devices/" + i.name + ".svg?v=" + assetV, label: i.label, type: picType(i) }; });
      Object.keys(meta.assets || {}).forEach(function (id) {
        var a = meta.assets[id];
        var am = a.meta || {};
        if (a.kind === "icon" && (!vend.value || vend.value === "*" || f || am.vendor === vend.value)) items.push({ icon: "asset:" + id, src: url("assets", id), label: a.name, type: am.type || "uploaded" });
      });
      items.forEach(function (it) {
        if (kind.value && it.type !== kind.value) return;
        if (f && f.split(/\s+/).some(function (w) { return it.label.toLowerCase().indexOf(w) < 0; })) return;
        var b = h("button", { type: "button", title: it.label });
        b.appendChild(h("img", { src: it.src, alt: "", loading: "lazy" }));
        b.appendChild(h("small", {}, vend.value && it.label.indexOf(vend.value + " ") === 0 ? it.label.slice(vend.value.length + 1) : it.label));
        b.addEventListener("click", function () { onPick(it.icon); });
        grid.appendChild(b);
      });
      if (!grid.firstChild) grid.appendChild(h("p", { "class": "muted small" }, T("No pictures match.")));
    }
    q.addEventListener("input", fill);
    var acts = h("div", { "class": "row" });
    acts.appendChild(button(T("Upload picture"), "secondary", function () { uploadPicture("icon", function (a) { onPick("asset:" + a.id); }); }));
    acts.appendChild(h("a", { href: "/pictures", target: "_blank", rel: "noopener", "class": "btn ghost sm" }, T("Picture library")));
    if (allowNone) acts.appendChild(button(T("Symbol (no picture)"), "ghost", function () { onPick(""); }));
    p.appendChild(acts);
    p.appendChild(q);
    var filters = h("div", { "class": "mp-filters" });
    filters.appendChild(vend);
    filters.appendChild(kind);
    p.appendChild(filters);
    p.appendChild(grid);
    fill();
  }
  function uploadPicture(kind, done) {
    upload.value = "";
    upload.onchange = function () {
      var f = upload.files && upload.files[0];
      if (!f) return;
      var fd = new FormData();
      fd.append("kind", kind);
      fd.append("file", f);
      setSave(T("Uploading…"));
      fetch("/assets", { method: "POST", credentials: "same-origin", body: fd })
        .then(function (r) { return r.json().then(function (j) { if (!r.ok) throw new Error(j.error || r.status); return j; }); })
        .then(function (a) { meta.assets[a.id] = a; setSave(dirty ? T("Unsaved changes") : ""); done(a); })
        .catch(function (e) { setSave(T("The picture could not be uploaded.") + " " + (e.message || "")); });
    };
    upload.click();
  }

  function showBackground() {
    var p = openPanel(T("Background"));
    var bg = layout.background || (layout.background = { asset: 0, x: 0, y: 0, w: 1000, opacity: 1 });
    var acts = h("div", { "class": "row" });
    acts.appendChild(button(T("Upload picture"), "secondary", function () { uploadPicture("background", function (a) { setBackground(a.id); }); }));
    if (bg.asset) acts.appendChild(button(T("Remove background"), "ghost", function () { bg.asset = 0; moveBg = false; render(); changed(); showBackground(); }));
    p.appendChild(acts);
    var mine = Object.keys(meta.assets || {}).filter(function (id) { return meta.assets[id].kind === "background"; });
    if (mine.length) {
      var grid = h("div", { "class": "mp-grid wide" });
      mine.forEach(function (id) {
        var b = h("button", { type: "button", "class": +id === bg.asset ? "on" : "" });
        b.appendChild(h("img", { src: url("assets", id), alt: "" }));
        b.appendChild(h("small", {}, meta.assets[id].name));
        b.addEventListener("click", function () { setBackground(+id); });
        grid.appendChild(b);
      });
      p.appendChild(grid);
    }
    if (!bg.asset) { p.appendChild(h("p", { "class": "muted small" }, T("Without a picture the map shows the dotted grid."))); return; }
    // the width on a log scale: from a small plan to a whole campus
    field(p, T("Size"), range(0, 1000, 1, Math.round(Math.log(bg.w / 100) / Math.log(400) * 1000), function (v) {
      var cx = bg.x + bg.w / 2, cy = bg.y + bgHeight() / 2;
      bg.w = Math.round(100 * Math.pow(400, v / 1000));
      bg.x = Math.round(cx - bg.w / 2); bg.y = Math.round(cy - bgHeight() / 2);
      render(); changed();
    }));
    field(p, T("Opacity"), range(0.1, 1, 0.05, bg.opacity || 1, function (v) { bg.opacity = v; render(); changed(); }));
    var mv = h("label", { "class": "check" });
    var cb = h("input", { type: "checkbox" });
    cb.checked = moveBg;
    cb.addEventListener("change", function () { moveBg = cb.checked; render(); });
    mv.appendChild(cb);
    mv.appendChild(document.createTextNode(T("Move background (drag it)")));
    p.appendChild(mv);
  }
  function setBackground(id) {
    var bg = layout.background || (layout.background = { asset: 0, x: 0, y: 0, w: 1000, opacity: 1 });
    var first = !bg.asset;
    bg.asset = id;
    if (first) {
      var r = svg.getBoundingClientRect(), c = toWorld(r.width / 2, r.height / 2);
      bg.w = Math.round(r.width / view.k * 0.9);
      bg.x = Math.round(c.x - bg.w / 2); bg.y = Math.round(c.y - bgHeight() / 2);
      bg.opacity = bg.opacity || 1;
    }
    render(); changed(); showBackground();
  }

  // ---- editing
  function freeSpot(w) {
    var r = svg.getBoundingClientRect(), c = toWorld(r.width / 2, r.height / 2), step = Math.max(110, (w || 80) + 40);
    for (var ring = 0; ring < 40; ring++) {
      for (var i = 0; i < Math.max(1, ring * 6); i++) {
        var a = i / Math.max(1, ring * 6) * Math.PI * 2;
        var x = Math.round((c.x + Math.cos(a) * ring * step) / 10) * 10, y = Math.round((c.y + Math.sin(a) * ring * step) / 10) * 10;
        var free = layout.nodes.every(function (n) { var b = box(n); return Math.abs(n.x - x) > (b.w + step) / 2 || Math.abs(n.y - y) > (b.h + 100) / 2; });
        if (free) return { x: x, y: y };
      }
    }
    return { x: Math.round(c.x), y: Math.round(c.y) };
  }
  function addNode(n, keepPanel) {
    n.id = nextTmp--;
    var p = freeSpot(box(n).w);
    n.x = p.x; n.y = p.y;
    layout.nodes.push(n);
    showHint("");
    if (!keepPanel) { selected = { type: "node", id: n.id }; render(); showSelection(); } else render();
    changed();
  }
  function removeSelected() {
    if (!editing || !selected) return;
    if (selected.type === "node") {
      var id = selected.id;
      layout.nodes.forEach(function (n) { if (n.parent === id) { n.parent = 0; n.u = 0; n.x += RACK_W + 60; } });
      layout.nodes = layout.nodes.filter(function (n) { return n.id !== id; });
      layout.links = layout.links.filter(function (k) { return k.a !== id && k.b !== id; });
      layout.ha.forEach(function (g) { g.nodes = g.nodes.filter(function (x) { return x !== id; }); });
      layout.ha = layout.ha.filter(function (g) { return g.nodes.length; });
    } else if (selected.type === "ha") {
      var hid = selected.id;
      layout.ha = layout.ha.filter(function (g) { return g.id !== hid; });
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
  // modelPicture finds the model picture whose model the device names in
  // its SNMP description ("RouterOS RB5009UG+S+IN"), the longest match
  function modelPicture(d) {
    if (!d || !d.descr) return "";
    var norm = function (s) { return s.toLowerCase().replace(/[^a-z0-9+]/g, ""); };
    var descr = norm(d.descr), best = "", bestLen = 0;
    libList.forEach(function (i) {
      if (!i.vendor) return;
      var key = norm(i.label.slice(i.vendor.length));
      if (key.length >= 5 && key.length > bestLen && descr.indexOf(key) >= 0) { best = i.name; bestLen = key.length; }
    });
    return best;
  }
  // libVendor: the picture library's manufacturer for a vendor SNMP names
  function libVendor(v) {
    if (!v) return "";
    v = v.toLowerCase();
    var best = "";
    libList.forEach(function (i) {
      var x = (i.vendor || "").toLowerCase();
      if (x && (x === v || x.indexOf(v) === 0 || v.indexOf(x) === 0) && (!best || i.vendor.length < best.length)) best = i.vendor;
    });
    return best;
  }
  // pictureVendor: the manufacturer the picture panel opens with — that of
  // the current model picture, or the vendor SNMP reports for the device
  function pictureVendor(n) {
    var cur = n.icon && n.icon.indexOf("lib:") === 0 ? lib[n.icon.slice(4)] : null;
    if (cur && cur.vendor) return cur.vendor;
    if (n.kind !== "device") return "";
    var d = live.devices[n.device] || deviceMeta(n.device) || {};
    return libVendor(d.vendor || (deviceMeta(n.device) || {}).vendor);
  }
  // a device gets a built-in picture fitting its kind when added
  var kindPicture = { router: "router-box", "switch": "switch-24", firewall: "firewall-box", server: "server-tower", pc: "pc-tower", printer: "printer", ups: "ups-tower", accesspoint: "ap-ceiling", camera: "camera-bullet" };
  function pickDevice() {
    var on = {};
    layout.nodes.forEach(function (n) { if (n.kind === "device") on[n.device] = true; });
    var items = (meta.devices || []).filter(function (d) { return !on[d.id]; }).map(function (d) { return { id: d.id, kind: d.kind, name: d.name, sub: d.address + " · " + T("kind:" + d.kind) }; });
    pickList(T("Add device"), items, T("All devices are on this map."), function (it) {
      var pic = modelPicture(deviceMeta(it.id)) || kindPicture[it.kind];
      addNode({ kind: "device", device: it.id, label: "", icon: pic && lib[pic] ? "lib:" + pic : "" }, true);
      pickDevice();
    });
  }
  function pickSubmap() {
    var items = (meta.maps || []).filter(function (m) { return String(m.id) !== String(mapID); }).map(function (m) { return { id: m.id, name: m.name }; });
    pickList(T("Add submap"), items, T("No other maps yet."), function (it) { addNode({ kind: "submap", submap: it.id, label: "" }); });
  }
  function addRack() {
    var p = openPanel(T("Add rack"));
    var nm = field(p, T("Rack name"), h("input", { value: T("Rack") + " " + (layout.nodes.filter(function (n) { return n.kind === "rack"; }).length + 1), maxlength: "200" }));
    var size = 42;
    field(p, T("Units"), select(rackSizes.map(function (u) { return [u, u + "U"]; }), size, function (v) { size = +v; }));
    p.appendChild(button(T("Add"), "primary", function () { addNode({ kind: "rack", label: nm.value, u: size }); }));
  }
  function showHint(text) { if (!hint) return; hint.textContent = text || ""; hint.hidden = !text; }
  function setConnect(on) {
    connecting = on; connectFrom = null;
    var b = root.querySelector('[data-act="connect"]');
    if (b) b.classList.toggle("on", on);
    showHint(on ? T("Click the first device, then the second.") : editing && !layout.nodes.length ? T("Drag devices to place them. Use Connect to draw a link and choose the ports.") : "");
    render();
  }
  function connectClick(n) {
    if (n.kind === "label" || n.kind === "rack") return;
    if (connectFrom === null) { connectFrom = n.id; render(); return; }
    if (connectFrom === n.id) { connectFrom = null; render(); return; }
    var k = { id: nextTmp--, a: connectFrom, b: n.id, a_if: 0, b_if: 0, a_slot: 0, b_slot: 0, kind: "ethernet", route: "straight", points: [], label: "" };
    layout.links.push(k);
    setConnect(false);
    selected = { type: "link", id: k.id };
    render(); showSelection(); changed();
  }

  // ---- path: only the devices and cables between two devices
  function onPath(set, id) { return path && path[set][id] ? " onpath" : ""; }
  function setPath(on) {
    var had = !!path;
    pathPick = on ? {} : null;
    path = null;
    connectFrom = null;
    var b = root.querySelector('[data-act="path"]');
    if (b) b.classList.toggle("on", on);
    showHint(on ? T("Click the first device, then the second; the map then shows only how they are connected.") : "");
    render();
    if (had) fit(true);
  }
  function pathClick(n) {
    if (n.kind === "label" || n.kind === "rack") return;
    if (pathPick.a === undefined) { pathPick.a = n.id; connectFrom = n.id; render(); return; }
    if (pathPick.a === n.id) return;
    var a = nodeByID(pathPick.a), found = findPath(pathPick.a, n.id);
    setPath(false);
    var b = root.querySelector('[data-act="path"]');
    if (!found) { showHint(T("These two are not connected on this map.")); return; }
    path = found;
    if (b) b.classList.add("on");
    showHint(nodeName(a) + " → " + nodeName(n) + ": " + found.hops + " " + (found.hops === 1 ? T("cable") : T("cables")) + (found.routes > 1 ? ", " + found.routes + " " + T("equal routes") : "") + ". " + T("Click Path or press Esc to show the whole map."));
    selected = null; closePanel();
    render(); fit(true);
  }
  // Paths work on places: a HA group is one place, so the way goes through
  // every member; cables between members of a group (and HA links) are no
  // way. Besides cables, a virtual machine reaches its hypervisor and the
  // hypervisor its physical servers.
  function placeOf(id) { var n = nodeByID(id), g = n && haOf(n); return g ? "g" + g.id : "n" + id; }
  function pathEdges() {
    var out = [];
    layout.links.forEach(function (k) { if (k.kind !== "ha") out.push({ a: k.a, b: k.b, k: k.id }); });
    layout.nodes.forEach(function (n) {
      var h = hostOf(n);
      if (h) out.push({ a: n.id, b: h.id });
      if (n.kind === "host") hostServers(n).forEach(function (s) { out.push({ a: n.id, b: s.id, s: n.id + ":" + s.id }); });
    });
    return out;
  }
  // findPath: every shortest way between two nodes (breadth first from both
  // ends: a step lies on a shortest way when it joins two places one step
  // apart on it), with the racks, hosts and HA groups along it
  function findPath(from, to) {
    var E = pathEdges(), adj = {};
    E.forEach(function (e) {
      e.pa = placeOf(e.a); e.pb = placeOf(e.b);
      if (e.pa === e.pb) return;
      (adj[e.pa] = adj[e.pa] || []).push({ p: e.pb, e: e });
      (adj[e.pb] = adj[e.pb] || []).push({ p: e.pa, e: e });
    });
    function bfs(src) {
      var d = {}, q = [src];
      d[src] = 0;
      while (q.length) {
        var x = q.shift();
        (adj[x] || []).forEach(function (o) { if (!(o.p in d)) { d[o.p] = d[x] + 1; q.push(o.p); } });
      }
      return d;
    }
    var A = placeOf(from), B = placeOf(to), res = { nodes: {}, links: {}, hostlinks: {}, hops: 0, routes: 1 };
    res.nodes[from] = res.nodes[to] = true;
    if (A !== B) {
      var da = bfs(A), db = bfs(B);
      if (!(B in da)) return null;
      var D = da[B], on = {}, cnt = {};
      E.forEach(function (e) {
        if (e.pa === e.pb) return;
        var fwd = da[e.pa] + 1 + db[e.pb] === D, back = da[e.pb] + 1 + db[e.pa] === D;
        if (!fwd && !back) return;
        e.from = fwd ? e.pa : e.pb; e.to = fwd ? e.pb : e.pa;
        res.nodes[e.a] = res.nodes[e.b] = true;
        if (e.k !== undefined) res.links[e.k] = true;
        if (e.s) res.hostlinks[e.s] = true;
        on[e.pa] = on[e.pb] = true;
        (cnt[e.from] = cnt[e.from] || []).push(e);
      });
      // routes: the number of shortest ways from device to device — the way
      // leaves a group through the member it came in by; cables: along one
      var ways = {}, order = Object.keys(on).sort(function (x, y) { return da[x] - da[y]; });
      ways[from] = 1;
      order.forEach(function (p) {
        (cnt[p] || []).forEach(function (e) {
          var a = e.pa === p ? e.a : e.b, b = e.pa === p ? e.b : e.a;
          if (ways[a]) ways[b] = (ways[b] || 0) + ways[a];
        });
      });
      var total = 0;
      Object.keys(ways).forEach(function (id) { if (placeOf(+id) === B) total += ways[id]; });
      res.routes = total || 1;
      for (var p = A; p !== B;) {
        var e = cnt[p][0];
        if (e.k !== undefined) res.hops++;
        p = e.to;
      }
      // every member of a HA group on the way
      layout.ha.forEach(function (g) { if (on["g" + g.id]) g.nodes.forEach(function (id) { res.nodes[id] = true; }); });
    }
    layout.nodes.forEach(function (n) { if (res.nodes[n.id] && n.parent) res.nodes[n.parent] = true; });
    return res;
  }

  // ---- VLANs: from the switches' and routers' ports; one can be shown alone
  var vlan = 0, vlanSel = root.querySelector("[data-vlan]"), vlanKey = "";
  function portVLAN(p, id) {
    if (!p || !p.vlans) return null;
    for (var i = 0; i < p.vlans.length; i++) if (p.vlans[i].id === id) return p.vlans[i];
    return null;
  }
  function findVLAN(list, id) {
    for (var i = 0; i < list.length; i++) if (list[i].id === id) return list[i];
    return null;
  }
  // linkVLANs: the VLANs set by hand on a cable, or else those its ports report
  function linkVLANs(k, s) {
    if (k.vlans && k.vlans.length) return k.vlans;
    var out = [];
    [s.a, s.b].forEach(function (p) { ((p && p.vlans) || []).forEach(function (v) { if (!findVLAN(out, v.id)) out.push(v); }); });
    return out.sort(function (x, y) { return x.id - y.id; });
  }
  function vlanText(list) {
    if (!list.length) return "";
    return list.map(function (v) { return v.id + (v.tagged ? " (T)" : " (U)"); }).join(", ");
  }
  // vlanMatch: the shown VLAN is in the list, tagged or untagged as chosen
  function vlanMatch(list) {
    var v = findVLAN(list || [], vlan);
    return !!v && (!vlanHow || (vlanHow === "t") === !!v.tagged);
  }
  function deviceInVLAN(n) {
    var ports = n.kind === "device" && live.ports ? live.ports[n.device] : null;
    for (var k in ports || {}) if (vlanMatch(ports[k].vlans)) return true;
    return layout.links.some(function (l) { return (l.a === n.id || l.b === n.id) && vlanMatch(l.vlans); });
  }
  // modeOf: access (one untagged VLAN), trunk (tagged VLANs; the untagged
  // one is the native VLAN) or hybrid (several untagged)
  function modeOf(list) {
    var t = [], u = [];
    (list || []).forEach(function (v) { (v.tagged ? t : u).push(v.id); });
    if (u.length > 1) return { mode: "hybrid", native: 0, tagged: t, untagged: u };
    if (t.length) return { mode: "trunk", native: u[0] || 0, tagged: t };
    if (u.length === 1) return { mode: "access", native: u[0], tagged: [] };
    return null;
  }
  // linkMode: the mode set by hand on a cable, or else that of a port
  function linkMode(k, s) {
    if (k.vlans && k.vlans.length) return modeOf(k.vlans);
    return modeOf(s.a && s.a.vlans && s.a.vlans.length ? s.a.vlans : s.b && s.b.vlans);
  }
  // ranges writes VLAN numbers compactly: "1-4, 10, 20-22"
  function ranges(ids) {
    ids = ids.slice().sort(function (a, b) { return a - b; });
    var out = [];
    for (var i = 0; i < ids.length;) {
      var j = i;
      while (j + 1 < ids.length && ids[j + 1] === ids[j] + 1) j++;
      out.push(j > i ? ids[i] + "-" + ids[j] : String(ids[i]));
      i = j + 1;
    }
    return out.join(", ");
  }
  function modeText(m, max) {
    if (!m) return "";
    var t = ranges(m.tagged);
    if (max && t.length > max) t = t.slice(0, t.lastIndexOf(",", max)) + " …";
    if (m.mode === "access") return T("Access") + " " + m.native;
    if (m.mode === "hybrid") return T("Hybrid") + " · U " + ranges(m.untagged) + (t ? " · T " + t : "");
    return T("Trunk") + (m.native ? " · " + T("native") + " " + m.native : "") + " · " + t;
  }
  // vlanMismatch: the two ends of a cable disagree (access against trunk, or
  // another native VLAN); a cable set by hand is taken as it is
  function vlanMismatch(k, s) {
    if (k.vlans && k.vlans.length) return "";
    var a = modeOf(s.a && s.a.vlans), b = modeOf(s.b && s.b.vlans);
    if (!a || !b) return "";
    var na = nodeName(nodeByID(k.a)), nb = nodeName(nodeByID(k.b));
    if ((a.mode === "access") !== (b.mode === "access"))
      return T("One end is an access port, the other a trunk:") + " " + na + " " + modeText(a, 20) + ", " + nb + " " + modeText(b, 20);
    if (a.native && b.native && a.native !== b.native)
      return T("The native VLAN differs:") + " " + na + " " + a.native + ", " + nb + " " + b.native;
    return "";
  }
  // syncVLANs fills the VLAN list when the VLANs on the map change
  function syncVLANs() {
    if (!vlanSel) return;
    var names = live.vlans || {}, seen = {};
    Object.keys(names).forEach(function (id) { seen[id] = true; });
    layout.links.forEach(function (k) { (k.vlans || []).forEach(function (v) { seen[v.id] = true; }); });
    var ids = Object.keys(seen).map(Number).sort(function (a, b) { return a - b; });
    var key = ids.map(function (id) { return id + ":" + (names[id] || ""); }).join(",");
    vlanSel.hidden = !ids.length;
    if (key === vlanKey) return;
    vlanKey = key;
    vlanSel.innerHTML = "";
    vlanSel.appendChild(h("option", { value: "0" }, T("All VLANs")));
    ids.forEach(function (id) { vlanSel.appendChild(h("option", { value: String(id) }, "VLAN " + id + (names[id] ? " · " + names[id] : ""))); });
    if (vlan && ids.indexOf(vlan) < 0) vlan = 0;
    vlanSel.value = String(vlan);
  }
  // parseVLANs reads "10, 20-25" (VLANs 1–4094)
  function parseVLANs(s) {
    var out = [];
    s.split(/[^0-9-]+/).forEach(function (part) {
      var r = part.split("-"), a = +r[0], b = r.length > 1 && r[1] !== "" ? +r[1] : a;
      if (!(a >= 1 && b <= 4094 && a <= b)) return;
      for (var id = a; id <= b; id++) if (out.indexOf(id) < 0) out.push(id);
    });
    return out;
  }
  // vlanFields: the VLAN mode of a cable set by hand (access VLAN, or a
  // trunk with its native and tagged VLANs) and the VXLAN VNI
  function vlanFields(p, k, s) {
    var m = modeOf(k.vlans) || { mode: "", native: 0, tagged: [] };
    var auto = modeOf(linkVLANs({}, s));
    var wrap = h("div"), acc = h("div"), trk = h("div");
    var mode = field(p, T("VLAN mode"), select([["", T("Automatic") + (auto ? " (" + modeText(auto, 24) + ")" : "")], ["access", T("Access")], ["trunk", T("Trunk")]], m.mode === "hybrid" ? "trunk" : m.mode, function () { show(); update(); }));
    p.appendChild(wrap);
    wrap.appendChild(acc);
    wrap.appendChild(trk);
    var accVLAN = field(acc, T("Access VLAN"), h("input", { value: m.mode === "access" ? String(m.native) : "", placeholder: "10", inputmode: "numeric" }));
    var native = field(trk, T("Native VLAN (untagged)"), h("input", { value: m.mode === "trunk" && m.native ? String(m.native) : "", placeholder: "1", inputmode: "numeric" }));
    var tagged = field(trk, T("Allowed VLANs (tagged)"), h("input", { value: ranges(m.tagged), placeholder: "10, 20-25" }));
    var vni = field(p, "VXLAN VNI", h("input", { value: k.vni ? String(k.vni) : "", placeholder: "10010", inputmode: "numeric" }));
    p.appendChild(h("p", { "class": "muted small" }, T("Automatic uses the VLANs the ports report.")));
    function show() { acc.hidden = mode.value !== "access"; trk.hidden = mode.value !== "trunk"; }
    function update() {
      var out = [];
      if (mode.value === "access") {
        var a = parseVLANs(accVLAN.value)[0];
        if (a) out.push({ id: a });
      } else if (mode.value === "trunk") {
        parseVLANs(tagged.value).forEach(function (id) { out.push({ id: id, tagged: true }); });
        var n = parseVLANs(native.value)[0];
        if (n) { out = out.filter(function (v) { return v.id !== n; }); out.push({ id: n }); }
      }
      k.vlans = out.sort(function (x, y) { return x.id - y.id; });
      var v = +vni.value.replace(/[^0-9]/g, "");
      k.vni = v >= 1 && v <= 16777215 ? v : 0;
      syncVLANs(); render(); changed();
    }
    show();
    [accVLAN, native, tagged, vni].forEach(function (x) { x.addEventListener("change", update); });
  }
  // the VLAN filter: one VLAN, tagged and untagged or only one of them
  var vlanHow = "", howSel = null;
  if (vlanSel) {
    howSel = h("select", { "class": "mapvlan", hidden: "", "aria-label": T("Tagged or untagged") });
    [["", T("Tagged and untagged")], ["t", T("Only tagged")], ["u", T("Only untagged")]].forEach(function (o) { howSel.appendChild(h("option", { value: o[0] }, o[1])); });
    vlanSel.parentNode.insertBefore(howSel, vlanSel.nextSibling);
    vlanSel.addEventListener("change", function () { vlan = +vlanSel.value; howSel.hidden = !vlan; render(); });
    howSel.addEventListener("change", function () { vlanHow = howSel.value; render(); });
  }

  // ---- layers: what the map shows (kept per browser)
  var LAYERS = [["devices", "Devices"], ["racks", "Racks"], ["hosts", "Hypervisors"], ["ha", "HA groups"], ["items", "Items"], ["submaps", "Submaps"], ["cables", "Cables"],
    ["ports", "Port names"], ["traffic", "Traffic"], ["vlans", "VLAN labels"], ["tunnels", "VXLAN tunnels"], ["names", "Names and addresses"], ["text", "Text"], ["background", "Background"]];
  var hidden = load("wz-map-layers") || {};
  var layersBox = root.querySelector("[data-layers]");
  function applyLayers() {
    LAYERS.forEach(function (l) { svg.classList.toggle("hide-" + l[0], !!hidden[l[0]]); });
    var b = root.querySelector('[data-act="layers"]');
    if (b) b.classList.toggle("on", LAYERS.some(function (l) { return hidden[l[0]]; }));
  }
  function toggleLayers() {
    if (!layersBox) return;
    var open = layersBox.hidden;
    layersBox.hidden = !open;
    var b = root.querySelector('[data-act="layers"]');
    if (b) b.setAttribute("aria-expanded", open ? "true" : "false");
    if (!open) return;
    layersBox.innerHTML = "";
    layersBox.appendChild(h("p", { "class": "small muted" }, T("Show on the map")));
    LAYERS.forEach(function (l) {
      var cb = h("input", { type: "checkbox" });
      cb.checked = !hidden[l[0]];
      cb.addEventListener("change", function () {
        if (cb.checked) delete hidden[l[0]]; else hidden[l[0]] = true;
        store("wz-map-layers", hidden); applyLayers();
      });
      var lb = h("label", { "class": "check" });
      lb.appendChild(cb);
      lb.appendChild(document.createTextNode(" " + T(l[1])));
      layersBox.appendChild(lb);
    });
    layersBox.appendChild(button(T("Show all"), "secondary", function () {
      hidden = {}; store("wz-map-layers", hidden); applyLayers(); toggleLayers(); toggleLayers();
    }));
  }
  applyLayers();
  document.addEventListener("click", function (ev) {
    if (layersBox && !layersBox.hidden && !layersBox.contains(ev.target) && !ev.target.closest('[data-act="layers"]')) toggleLayers();
  });
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
        var exists = layout.links.some(function (k) { return (k.a === na.id && k.b === nb.id && k.a_if === sg.a_if) || (k.a === nb.id && k.b === na.id && k.b_if === sg.a_if); });
        if (exists) return;
        var b = h("button", { type: "button" });
        b.appendChild(h("b", {}, nodeName(na) + " " + (sg.a_name || "") + "  ↔  " + nodeName(nb) + " " + (sg.b_name || "")));
        b.appendChild(h("small", {}, sg.via));
        b.addEventListener("click", function () {
          layout.links.push({ id: nextTmp--, a: na.id, b: nb.id, a_if: sg.a_if || 0, b_if: sg.b_if || 0, a_slot: guessSlot(na, sg.a_name), b_slot: guessSlot(nb, sg.b_name), kind: "ethernet", route: "straight", points: [], label: "" });
          b.parentNode.removeChild(b);
          render(); changed();
        });
        var li = h("li"); li.appendChild(b); ul.appendChild(li); n++;
      });
      if (!n) ul.appendChild(h("li", { "class": "muted small" }, T("No suggestions.")));
    }).catch(function () { ul.appendChild(h("li", { "class": "muted small" }, T("No suggestions."))); });
  }

  // ---- saving: only on Save; temporary ids are replaced by the server's
  var changeGen = 0;
  function changed() {
    if (kioskBase) return;
    dirty = true;
    changeGen++;
    setSave(T("Unsaved changes"));
    syncSaveButtons();
  }
  function setSave(t) { if (!saveState) return; saveState.textContent = t; saveState.hidden = !t; }
  // While there are unsaved changes, Save and Cancel replace Done.
  function syncSaveButtons() {
    var s = root.querySelector('[data-act="save"]'), c = root.querySelector('[data-act="discard"]'), e = root.querySelector('[data-act="edit"]');
    if (s) { s.hidden = !editing; s.disabled = !dirty || saving; }
    if (c) c.hidden = !editing || !dirty;
    if (e) e.hidden = editing && dirty;
  }
  function discard() {
    if (!dirty || !window.confirm(T("Discard the changes made since the last save?"))) return;
    var v = { x: view.x, y: view.y, k: view.k };
    dirty = false;
    setSave("");
    loadMap(mapID, false).then(function () { view = v; applyView(); syncSaveButtons(); });
  }
  window.addEventListener("beforeunload", function (ev) {
    if (dirty || saving) { ev.preventDefault(); ev.returnValue = ""; }
  });
  function save() {
    if (saving || !dirty) return;
    saving = true;
    var gen = changeGen;
    setSave(T("Saving…"));
    syncSaveButtons();
    layout.nodes = layout.nodes.filter(function (n) { return n.kind !== "label" || (n.label && n.label.trim()); });
    var sent = JSON.parse(JSON.stringify(layout));
    sent.ha = (sent.ha || []).filter(function (g) { return g.nodes.length >= 2; }); // a group being made waits for its second member
    fetch("/maps/" + mapID + "/layout", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "Accept": "application/json" }, body: JSON.stringify(sent) })
      .then(function (r) { return r.json().then(function (j) { if (!r.ok) throw new Error(j.error || r.status); return j; }); })
      .then(function (out) {
        var ids = {}, lids = {};
        sent.nodes.forEach(function (n, i) { if (out.nodes[i]) ids[n.id] = out.nodes[i].id; });
        sent.links.forEach(function (k, i) { if (out.links[i]) lids[k.id] = out.links[i].id; });
        var hids = {};
        sent.ha.forEach(function (g, i) { if (out.ha && out.ha[i]) hids[g.id] = out.ha[i].id; });
        layout.ha.forEach(function (g) {
          g.nodes = g.nodes.map(function (id) { return ids[id] !== undefined ? ids[id] : id; });
          if (hids[g.id] !== undefined) g.id = hids[g.id];
        });
        layout.nodes.forEach(function (n) { if (ids[n.id] !== undefined) n.id = ids[n.id]; if (n.parent && ids[n.parent] !== undefined) n.parent = ids[n.parent]; });
        layout.links.forEach(function (k) {
          if (ids[k.a] !== undefined) k.a = ids[k.a];
          if (ids[k.b] !== undefined) k.b = ids[k.b];
          if (lids[k.id] !== undefined) k.id = lids[k.id];
        });
        if (selected) { var m = selected.type === "node" ? ids : selected.type === "ha" ? hids : lids; if (m[selected.id] !== undefined) selected.id = m[selected.id]; }
        if (connectFrom !== null && ids[connectFrom] !== undefined) connectFrom = ids[connectFrom];
        saving = false;
        if (changeGen === gen) { dirty = false; setSave(T("Saved")); } else setSave(T("Unsaved changes"));
        syncSaveButtons();
        render();
        refresh();
      })
      .catch(function (e) { saving = false; syncSaveButtons(); setSave(T("Could not save.") + " " + (e.message || "")); });
  }

  // ---- pointer handling: drag nodes, racks, bends, background; pan; pinch
  var pointers = {}, drag = null, pinch = null, moved = false, lastTap = null;
  // hostAt finds the hypervisor host box under a point
  function hostAt(x, y, self) {
    for (var i = 0; i < layout.nodes.length; i++) {
      var r = layout.nodes[i];
      if (r.kind !== "host" || r === self) continue;
      var b = box(r);
      if (Math.abs(x - r.x) <= b.w / 2 && Math.abs(y - r.y) <= b.h / 2) return r;
    }
    return null;
  }
  function rackAt(x, y, self) {
    for (var i = 0; i < layout.nodes.length; i++) {
      var r = layout.nodes[i];
      if (r.kind !== "rack" || r === self) continue;
      var top = rackTop(r);
      if (Math.abs(x - r.x) <= RACK_W / 2 && y >= top - 10 && y <= top + r.u * UH + 10) return r;
    }
    return null;
  }
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
    var t = ev.target, p = pointer(ev), w = toWorld(p.x, p.y);
    var handle = t.closest ? t.closest("[data-handle]") : null;
    var addh = t.closest ? t.closest("[data-add]") : null;
    var nodeEl = t.closest ? t.closest("[data-node]") : null;
    var linkEl = t.closest ? t.closest("[data-link]") : null;
    // Double clicks and taps are recognised here: the pointer capture makes
    // the browser's dblclick land on the whole drawing, not on what was hit.
    var key = handle ? "h" + handle.getAttribute("data-handle") : addh ? "a" : nodeEl ? "n" + nodeEl.getAttribute("data-node") : linkEl ? "l" : "bg";
    var now = Date.now(), dbl = lastTap && lastTap.key === key && now - lastTap.t < 500 && Math.abs(p.x - lastTap.x) + Math.abs(p.y - lastTap.y) < 12;
    lastTap = dbl ? null : { key: key, t: now, x: p.x, y: p.y };
    if (dbl && doubleTap(handle, nodeEl, linkEl || addh, p)) { drag = null; return; }
    if (editing && handle && selected) {
      drag = { type: "bend", k: linkByID(selected.id), i: +handle.getAttribute("data-handle"), sx: p.x, sy: p.y };
    } else if (editing && addh && selected) {
      var k = linkByID(selected.id), i = +addh.getAttribute("data-add");
      k.points = k.points || [];
      k.points.splice(i, 0, [Math.round(w.x / 5) * 5, Math.round(w.y / 5) * 5]);
      drag = { type: "bend", k: k, i: i, sx: p.x, sy: p.y, added: true };
      render();
    } else if (nodeEl) {
      var n = nodeByID(+nodeEl.getAttribute("data-node"));
      drag = { type: "node", n: n, dx: n.x - w.x, dy: n.y - w.y, sx: p.x, sy: p.y, vx: view.x, vy: view.y };
    } else if (linkEl) {
      drag = { type: "link", id: +linkEl.getAttribute("data-link"), sx: p.x, sy: p.y };
    } else if (t.closest && t.closest("[data-ha]")) {
      drag = { type: "ha", id: +t.closest("[data-ha]").getAttribute("data-ha"), sx: p.x, sy: p.y, vx: view.x, vy: view.y };
    } else if (editing && moveBg && t.classList && t.classList.contains("bgimg")) {
      drag = { type: "bg", dx: layout.background.x - w.x, dy: layout.background.y - w.y, sx: p.x, sy: p.y };
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
      var k = Math.min(8, Math.max(0.05, pinch.k * d / pinch.d));
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
    var w = toWorld(p.x, p.y);
    if (drag.type === "pan" || (drag.type === "node" && (!editing || connecting))) {
      view.x = drag.vx + p.x - drag.sx;
      view.y = drag.vy + p.y - drag.sy;
      applyView();
    } else if (drag.type === "node") {
      var n = drag.n;
      if (n.parent) n.parent = 0; // lifted out of its rack; dropped below
      n.x = Math.round((w.x + drag.dx) / 10) * 10;
      n.y = Math.round((w.y + drag.dy) / 10) * 10;
      drag.changed = true;
      render();
    } else if (drag.type === "bend") {
      drag.k.points[drag.i] = [Math.round(w.x / 5) * 5, Math.round(w.y / 5) * 5];
      drag.changed = true;
      render();
    } else if (drag.type === "bg") {
      layout.background.x = Math.round(w.x + drag.dx);
      layout.background.y = Math.round(w.y + drag.dy);
      drag.changed = true;
      render();
    }
  });
  function endPointer(ev) {
    delete pointers[ev.pointerId];
    if (pinch) { if (Object.keys(pointers).length < 2) pinch = null; drag = null; return; }
    var d = drag;
    drag = null;
    if (!d) return;
    if (d.type === "node" && d.changed) {
      // dropped on a rack: mounted on the units under it (or the nearest free)
      var n = d.n, guest = n.kind === "device" || n.kind === "item", r = n.kind !== "rack" && n.kind !== "label" && n.kind !== "host" ? rackAt(n.x, n.y, n) : null;
      var hv = guest && !r ? hostAt(n.x, n.y, n) : null;
      if (hv) {
        // dropped on a hypervisor host: runs on it, at the place it was dropped
        var gs = guestsOf(hv).filter(function (c) { return c !== n; });
        n.parent = hv.id; n.u = 1e6; // counted while measuring the box
        var cols = hostCols(gs.length + 1), hb = box(hv), best = gs.length, bestD = Infinity;
        for (var k = 0; k <= gs.length; k++) { // the nearest place
          var sx = hv.x - hb.w / 2 + 10 + (k % cols) * HOST_TILE_W + HOST_TILE_W / 2, sy = hv.y - hb.h / 2 + HOST_HEAD + Math.floor(k / cols) * HOST_TILE_H + 42;
          var dd = Math.hypot(n.x - sx, n.y - sy);
          if (dd < bestD) { bestD = dd; best = k; }
        }
        gs.splice(best, 0, n);
        n.parent = hv.id;
        gs.forEach(function (c, i) { c.u = i + 1; });
      } else if (r) {
        var hgt = unitsOf(n), top = rackTop(r);
        var want = Math.max(1, Math.min(r.u - hgt + 1, Math.round(r.u - (n.y - top) / UH - hgt / 2 + 1)));
        n.parent = r.id;
        n.u = unitFree(r, want, hgt, n) ? want : freeUnit(r, hgt, n) || want;
      } else if (n.kind !== "rack") {
        n.u = 0;
      }
      render(); changed();
      if (selected && selected.type === "node" && selected.id === n.id) showSelection();
      return;
    }
    if ((d.type === "bend" || d.type === "bg") && (d.changed || d.added)) { changed(); return; }
    if (moved || d.type === "bend") return; // a click on a bend keeps the cable selected
    if (d.type === "node") {
      if (pathPick) { pathClick(d.n); return; }
      if (connecting) { connectClick(d.n); return; }
      selected = { type: "node", id: d.n.id };
    } else if (d.type === "link") {
      selected = { type: "link", id: d.id };
    } else if (d.type === "ha") {
      selected = { type: "ha", id: d.id };
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
  // doubleTap: a bend point is removed, a device or submap opens (not while
  // editing), the empty drawing zooms in. Reports whether it acted.
  function doubleTap(handle, nodeEl, linkEl, p) {
    if (handle) {
      if (!editing || !selected || selected.type !== "link") return false;
      linkByID(selected.id).points.splice(+handle.getAttribute("data-handle"), 1);
      render(); changed();
      return true;
    }
    if (nodeEl) {
      if (kioskBase) return false;
      var n = nodeByID(+nodeEl.getAttribute("data-node"));
      var to = n && n.kind === "device" ? "/devices/" + n.device + fromMap() : n && n.kind === "submap" ? "/maps/" + n.submap : "";
      if (!to || (editing && n.kind === "submap")) return false;
      // while editing: in a new tab, so the unsaved drawing stays
      if (editing) window.open(to, "_blank", "noopener"); else location.href = to;
      return true;
    }
    if (linkEl) return false;
    zoomAt(p.x, p.y, 1.6);
    return true;
  }
  svg.addEventListener("dblclick", function (ev) { ev.preventDefault(); });
  document.addEventListener("keydown", function (ev) {
    if ((ev.ctrlKey || ev.metaKey) && (ev.key === "s" || ev.key === "S") && editing) { ev.preventDefault(); save(); return; }
    if (ev.target && /INPUT|SELECT|TEXTAREA/.test(ev.target.tagName)) return;
    if ((ev.key === "Delete" || ev.key === "Backspace") && editing && selected) { ev.preventDefault(); removeSelected(); }
    if (ev.key === "Escape") { if (pathPick || path) setPath(false); else if (connecting) setConnect(false); else { selected = null; closePanel(); render(); } }
    var r = svg.getBoundingClientRect();
    if (ev.key === "+" || ev.key === "=") zoomAt(r.width / 2, r.height / 2, 1.25);
    if (ev.key === "-") zoomAt(r.width / 2, r.height / 2, 0.8);
  });

  // ---- toolbar
  root.addEventListener("click", function (ev) {
    var b = ev.target.closest("[data-act]");
    if (!b) return;
    var act = b.getAttribute("data-act"), r = svg.getBoundingClientRect();
    function zoomBy(f) { var k = Math.min(8, Math.max(0.05, view.k * f)); animateTo({ k: k, x: r.width / 2 - (r.width / 2 - view.x) * k / view.k, y: r.height / 2 - (r.height / 2 - view.y) * k / view.k }); }
    if (act === "zoom-in") zoomBy(1.4);
    else if (act === "zoom-out") zoomBy(1 / 1.4);
    else if (act === "fit") fit(true);
    else if (act === "fullscreen") fullscreen();
    else if (act === "edit") setEditing(!editing);
    else if (act === "save") save();
    else if (act === "discard") discard();
    else if (act === "add-device") { setConnect(false); pickDevice(); }
    else if (act === "add-item") { setConnect(false); pickPicture(function (icon) { var p = picOf({ icon: icon }); addNode({ kind: "item", icon: icon, label: p && p.label ? p.label : "" }); }, false); }
    else if (act === "add-rack") { setConnect(false); addRack(); }
    else if (act === "add-host") { setConnect(false); addHost(); }
    else if (act === "add-submap") { setConnect(false); pickSubmap(); }
    else if (act === "add-text") { setConnect(false); addNode({ kind: "label", label: T("Text") }); }
    else if (act === "background") { setConnect(false); selected = null; render(); showBackground(); }
    else if (act === "path") { setConnect(false); setPath(!(pathPick || path)); }
    else if (act === "layers") toggleLayers();
    else if (act === "connect") { setPath(false); setConnect(!connecting); }
    else if (act === "suggest") { setConnect(false); suggestions(); }
    else if (act === "delete") removeSelected();
  });
  function fullscreen() {
    if (document.fullscreenElement) { document.exitFullscreen(); return; }
    if (root.requestFullscreen) root.requestFullscreen().catch(function () { /* not allowed */ });
  }
  document.addEventListener("fullscreenchange", function () { setTimeout(function () { size(); fit(true); }, 150); });
  function setEditing(on) {
    editing = !!(on && meta && meta.can_edit && !kioskBase);
    Array.prototype.forEach.call(root.querySelectorAll("[data-edit-only]"), function (e) { e.hidden = !editing; });
    var l = root.querySelector("[data-edit-label]");
    if (l) l.textContent = editing ? T("Done") : T("Edit");
    syncSaveButtons();
    if (!editing) { moveBg = false; setConnect(false); }
    else showHint(!layout.nodes.length ? T("Drag devices to place them. Use Connect to draw a link and choose the ports.") : "");
    if (!kioskBase) try { history.replaceState(null, "", location.pathname + (editing ? "?edit=1" : "")); } catch (e) { /* ignore */ }
    render(); showSelection();
  }

  // ---- data
  function refresh() {
    return fetch(url("live", mapID), { credentials: "same-origin", headers: { "Accept": "application/json" } })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
      .then(function (d) { live = d; syncVLANs(); if (!drag) { render(); if (selected && !editing && !panel.contains(document.activeElement)) showSelection(); } })
      .catch(function () { /* next round */ });
  }
  function loadMap(id, restoreView) {
    mapID = id;
    return fetch(url("layout", id), { credentials: "same-origin", headers: { "Accept": "application/json" } })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
      .then(function (d) {
        meta = d;
        meta.assets = meta.assets || {};
        layout = d.layout || { nodes: [], links: [] };
        layout.ha = layout.ha || [];
        layout.links.forEach(function (k) { k.points = k.points || []; });
        selected = null; closePanel();
        var saved = restoreView ? load("wz-map-view-" + mapID) : null;
        place();
        if (saved && isFinite(saved.k)) { view = saved; applyView(); } else fit(false);
        if (kioskBase) kioskTitle(d.name);
        else setEditing(editing);
        return refresh();
      });
  }

  // ---- kiosk: full window, maps change by themselves, a clock
  var kioskIndex = 0;
  function kioskTitle(name) {
    var t = root.querySelector("[data-kiosk-title]");
    if (t) t.textContent = name;
    var dots = root.querySelector("[data-kiosk-dots]");
    if (dots) {
      dots.innerHTML = "";
      if (kioskMaps.length > 1) kioskMaps.forEach(function (_, i) { dots.appendChild(h("i", { "class": i === kioskIndex ? "on" : "" })); });
    }
  }
  function kioskClock() {
    var c = root.querySelector("[data-kiosk-clock]");
    if (!c) return;
    var d = new Date();
    c.textContent = d.toLocaleDateString() + "  " + d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }

  // the map fills the window below the toolbar (banners above vary)
  function size() { root.style.height = Math.max(380, window.innerHeight - root.getBoundingClientRect().top - (kioskBase || document.fullscreenElement ? 0 : 14)) + "px"; }
  size();
  window.addEventListener("resize", function () { size(); applyView(); });

  fetch("/static/devices/index.json?v=" + assetV, { credentials: "same-origin" })
    .then(function (r) { return r.json(); })
    .catch(function () { return []; })
    .then(function (list) {
      libList = list || [];
      libList.forEach(function (i) { lib[i.name] = i; });
      if (kioskBase) {
        var th = root.getAttribute("data-theme");
        if (th) document.documentElement.setAttribute("data-theme", th);
        kioskClock();
        setInterval(kioskClock, 10000);
        var rot = Math.max(5, parseInt(root.getAttribute("data-rotate"), 10) || 30) * 1000;
        if (kioskMaps.length > 1) setInterval(function () {
          kioskIndex = (kioskIndex + 1) % kioskMaps.length;
          loadMap(kioskMaps[kioskIndex], false);
        }, rot);
        // a display runs for weeks: reload now and then to pick up new code
        setTimeout(function () { location.reload(); }, 6 * 3600 * 1000);
        return loadMap(mapID, false);
      }
      return loadMap(mapID, true);
    });
  setInterval(function () { if (!document.hidden && !saving) refresh(); }, 10000);
})();

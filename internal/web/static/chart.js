// WatchZit graphs: time series drawn on a canvas (no chart library; the
// CSP allows only scripts of this console). Each <figure class="graph"
// data-series="ID,ID" data-labels="In|Out" data-range=24h data-unit=ms>
// draws its series as lines in one graph; a single series also gets the
// minimum–maximum band of summarised data. A legend lists last, minimum,
// average and maximum. Drag across a graph to zoom in, double-click to go
// back; hover shows the values.
(function () {
  "use strict";
  var figs = Array.prototype.slice.call(document.querySelectorAll("figure.graph[data-series]"));
  if (!figs.length) return;

  // Times are shown in the server's time zone, like the rest of the
  // console (the sidebar clock carries its offset).
  var clock = document.getElementById("clock");
  var zoneOff = clock ? parseInt(clock.getAttribute("data-off"), 10) || 0 : -new Date().getTimezoneOffset() * 60;

  function css(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }
  function pad(n) { return (n < 10 ? "0" : "") + n; }
  function zdate(t) { return new Date((t + zoneOff) * 1000); }
  function fmtTime(t, span) {
    var d = zdate(t);
    var hm = pad(d.getUTCHours()) + ":" + pad(d.getUTCMinutes());
    if (span <= 86400 * 1.5) return hm;
    var dm = pad(d.getUTCDate()) + "." + pad(d.getUTCMonth() + 1) + ".";
    return span <= 86400 * 10 ? dm + " " + hm : dm;
  }
  function fmtFull(t) {
    var d = zdate(t);
    return d.getUTCFullYear() + "-" + pad(d.getUTCMonth() + 1) + "-" + pad(d.getUTCDate()) + " " + pad(d.getUTCHours()) + ":" + pad(d.getUTCMinutes());
  }
  function num(v) {
    var a = Math.abs(v);
    if (a >= 100) return v.toFixed(0);
    if (a >= 10) return v.toFixed(1);
    return v.toFixed(2);
  }
  // Units that grow by prefixes: bit/s in thousands, bytes in 1024s.
  var scaled = { "bit/s": [1000, ["bit/s", "kbit/s", "Mbit/s", "Gbit/s", "Tbit/s"]], "B": [1024, ["B", "KiB", "MiB", "GiB", "TiB", "PiB"]] };
  function fmtVal(v, unit) {
    if (v === null || v === undefined) return "—";
    var sc = scaled[unit];
    if (sc) {
      if (v === 0) return "0 " + sc[1][0];
      var i = 0;
      while (Math.abs(v) >= sc[0] && i < sc[1].length - 1) { v /= sc[0]; i++; }
      return num(v) + " " + sc[1][i];
    }
    return num(v) + (unit ? " " + unit : "");
  }
  // niceStep returns a round step (1, 2, 5 × 10^n) for about n ticks; for
  // prefixed units the step is round in the prefix (e.g. 2 Mbit/s).
  function niceStep(range, n, unit) {
    var base = 1, sc = scaled[unit];
    if (sc) { while (range / base >= sc[0] * n && base < Math.pow(sc[0], 5)) base *= sc[0]; }
    var raw = range / base / n, mag = Math.pow(10, Math.floor(Math.log10(raw))), r = raw / mag;
    return (r <= 1 ? 1 : r <= 2 ? 2 : r <= 5 ? 5 : 10) * mag * base;
  }
  var timeSteps = [60, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400, 172800, 604800, 1209600, 2592000];

  function Graph(fig) {
    this.fig = fig;
    this.canvas = fig.querySelector("canvas");
    this.tip = fig.querySelector(".graph-tip");
    this.ids = fig.getAttribute("data-series").split(",");
    this.labels = (fig.getAttribute("data-labels") || "").split("|");
    this.range = fig.getAttribute("data-range") || "24h";
    this.unit = fig.getAttribute("data-unit") || "";
    // how the user set the graph up: kind, stacking, fixed axis ends, thresholds
    this.type = fig.getAttribute("data-type") || "line";
    this.stack = fig.getAttribute("data-stack") === "1";
    this.fixed = { min: numAttr(fig, "data-min"), max: numAttr(fig, "data-max"), warn: numAttr(fig, "data-warn"), crit: numAttr(fig, "data-crit") };
    this.colors = (fig.getAttribute("data-colors") || "").split("|");
    this.applySize();
    this.addExpand();
    this.zoom = null; // {from, to} in Unix seconds
    this.data = null; // one result per series
    this.drag = null;
    this.hover = null; // time under the mouse
    this.legend = this.makeLegend();
    this.bind();
  }

  function numAttr(el, name) {
    var v = el.getAttribute(name);
    return v === null || v === "" || isNaN(+v) ? null : +v;
  }

  // addExpand: a button that shows the graph over the whole window (Esc or
  // the button again goes back)
  Graph.prototype.addExpand = function () {
    var g = this, cap = this.fig.querySelector("figcaption");
    if (!cap) return;
    var b = document.createElement("button");
    b.type = "button";
    b.className = "graph-expand";
    b.title = b.textContent = "⤢";
    b.setAttribute("aria-label", "⤢");
    b.addEventListener("click", function () { g.setFull(!g.fig.classList.contains("full")); });
    cap.appendChild(b);
  };
  Graph.prototype.setFull = function (on) {
    this.fig.classList.toggle("full", on);
    document.body.classList.toggle("graph-full", on);
    var g = this;
    setTimeout(function () { g.load(); }, 30); // more points for the wider canvas
  };
  document.addEventListener("keydown", function (ev) {
    if (ev.key !== "Escape") return;
    graphs.forEach(function (g) { if (g.fig.classList.contains("full")) g.setFull(false); });
  });

  Graph.prototype.makeLegend = function () {
    var heads = (this.fig.getAttribute("data-legend") || "Last|Min|Avg|Max").split("|");
    var t = document.createElement("table");
    t.className = "graph-legend";
    var tr = document.createElement("tr");
    ["", heads[0], heads[1], heads[2], heads[3]].forEach(function (h) { var th = document.createElement("th"); th.textContent = h; tr.appendChild(th); });
    t.appendChild(tr);
    this.fig.appendChild(t);
    return t;
  };

  Graph.prototype.color = function (i) {
    var c = this.colors && this.colors[i];
    if (c && /^#[0-9a-fA-F]{6}$/.test(c)) return c;
    return css("--series-" + (i % 4 + 1)) || css("--accent") || "#4f46e5";
  };
  // applySize: the columns and height set by dragging the corner
  Graph.prototype.applySize = function () {
    var cols = numAttr(this.fig, "data-cols"), hh = numAttr(this.fig, "data-h");
    var n = this.fig.parentNode ? (getComputedStyle(this.fig.parentNode).gridTemplateColumns || "").split(" ").filter(Boolean).length : 1;
    this.fig.style.gridColumn = cols ? "span " + Math.max(1, Math.min(cols, n || 1)) : "";
    this.canvas.style.height = hh ? hh + "px" : "";
  };

  Graph.prototype.url = function (id) {
    var w = Math.max(100, Math.round(this.canvas.clientWidth || 600));
    var u = "/series/" + encodeURIComponent(id) + "?points=" + Math.min(3000, w);
    if (this.zoom) return u + "&from=" + Math.floor(this.zoom.from) + "&to=" + Math.ceil(this.zoom.to);
    return u + "&range=" + encodeURIComponent(this.range);
  };

  Graph.prototype.load = function () {
    var g = this;
    Promise.all(this.ids.map(function (id) {
      return fetch(g.url(id), { credentials: "same-origin", headers: { "Accept": "application/json" } })
        .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); });
    }))
      .then(function (ds) { g.data = ds; g.draw(); g.fillLegend(); })
      .catch(function () { g.data = null; g.draw(); g.fillLegend(); });
  };

  Graph.prototype.fillLegend = function () {
    var g = this, t = this.legend;
    while (t.rows.length > 1) t.deleteRow(1);
    (this.data || []).forEach(function (d, i) {
      var last = null, lo = null, hi = null, sum = 0, n = 0;
      var band = d.min && d.min.length === d.t.length && d.step > 0;
      for (var k = 0; k < d.t.length; k++) {
        var v = d.avg[k];
        if (v === null) continue;
        last = v; sum += v; n++;
        var a = band && d.min[k] !== null ? d.min[k] : v, b = band && d.max[k] !== null ? d.max[k] : v;
        if (lo === null || a < lo) lo = a;
        if (hi === null || b > hi) hi = b;
      }
      var tr = t.insertRow(-1);
      var name = tr.insertCell(-1), sw = document.createElement("i");
      sw.style.background = g.color(i);
      name.appendChild(sw);
      name.appendChild(document.createTextNode(g.labels[i] || ""));
      [last, lo, n ? sum / n : null, hi].forEach(function (v) { tr.insertCell(-1).textContent = fmtVal(v, g.unit); });
    });
  };

  Graph.prototype.layout = function () {
    var c = this.canvas, dpr = window.devicePixelRatio || 1;
    var w = c.clientWidth || 600, h = c.clientHeight || 200;
    if (c.width !== Math.round(w * dpr) || c.height !== Math.round(h * dpr)) {
      c.width = Math.round(w * dpr);
      c.height = Math.round(h * dpr);
    }
    return { w: w, h: h, dpr: dpr, l: 64, r: 10, t: 10, b: 24 };
  };

  // segments splits a series where samples are missing for longer than
  // 2.5 intervals, so outages show as gaps.
  function segments(d) {
    var gap = d.step > 0 ? d.step * 2.5 : 0;
    if (!gap && d.t.length > 2) {
      var ds = [];
      for (var j = 1; j < d.t.length; j++) ds.push(d.t[j] - d.t[j - 1]);
      ds.sort(function (p, q) { return p - q; });
      gap = ds[Math.floor(ds.length / 2)] * 2.5;
    }
    var segs = [], cur = [];
    for (var m = 0; m < d.t.length; m++) {
      if (d.avg[m] === null || (cur.length && gap && d.t[m] - d.t[cur[cur.length - 1]] > gap)) {
        if (cur.length) segs.push(cur);
        cur = [];
        if (d.avg[m] === null) continue;
      }
      cur.push(m);
    }
    if (cur.length) segs.push(cur);
    return segs;
  }

  // stackTops: for stacked graphs, each series added to the ones before it
  // (series of one check share their times; others are matched by index)
  function stackTops(ds) {
    var out = [], n = ds[0].t.length;
    ds.forEach(function (d, si) {
      var row = [];
      for (var i = 0; i < n; i++) {
        var v = d.avg[i] === null || d.avg[i] === undefined ? 0 : d.avg[i];
        row.push((si ? out[si - 1][i] : 0) + v);
      }
      out.push(row);
    });
    return out;
  }

  function nearest(d, t) {
    if (!d.t.length) return null;
    var lo = 0, hi = d.t.length - 1;
    while (hi - lo > 1) { var mid = (lo + hi) >> 1; if (d.t[mid] < t) lo = mid; else hi = mid; }
    return Math.abs(d.t[lo] - t) <= Math.abs(d.t[hi] - t) ? lo : hi;
  }

  Graph.prototype.draw = function () {
    var L = this.layout(), ctx = this.canvas.getContext("2d"), g = this;
    ctx.setTransform(L.dpr, 0, 0, L.dpr, 0, 0);
    ctx.clearRect(0, 0, L.w, L.h);
    ctx.font = "12px " + (css("--font") || "system-ui, sans-serif");
    var muted = css("--muted") || "#64748b", line = css("--line") || "#e2e8f0";
    var ds = this.data;
    var pw = L.w - L.l - L.r, ph = L.h - L.t - L.b;
    this.L = L;
    var any = ds && ds.some(function (d) { return d.t && d.t.length; });
    if (!any) {
      ctx.fillStyle = muted;
      ctx.textAlign = "center";
      ctx.fillText(ds ? this.fig.getAttribute("data-nodata") || "No data" : "…", L.l + pw / 2, L.t + ph / 2);
      return;
    }
    var from = ds[0].from, to = ds[0].to, span = Math.max(1, to - from);
    var single = ds.length === 1 && this.type === "line";
    var tops = this.stack ? stackTops(ds) : null, fx = this.fixed, kind = this.type;
    var val = function (si, i) { return tops ? (tops[si][i] === undefined ? null : tops[si][i]) : ds[si].avg[i]; };
    var lo = Infinity, hi = -Infinity;
    ds.forEach(function (d, si) {
      var band = single && d.min && d.min.length === d.t.length && d.step > 0;
      for (var i = 0; i < d.t.length; i++) {
        var a = band ? d.min[i] : val(si, i), b = band ? d.max[i] : val(si, i);
        if (a !== null && a < lo) lo = a;
        if (b !== null && b > hi) hi = b;
      }
    });
    [fx.warn, fx.crit].forEach(function (v) { if (v !== null) { lo = Math.min(lo, v); hi = Math.max(hi, v); } });
    if (!isFinite(lo)) { lo = 0; hi = 1; }
    if (kind !== "line" || tops) lo = Math.min(lo, 0);
    if (lo > 0 && lo < hi * 0.5) lo = 0; // start at zero when it is near
    if (this.unit === "%") { lo = Math.min(lo, 0); hi = Math.max(hi, lo + 1); }
    if (scaled[this.unit] && lo > 0) lo = 0;
    if (hi === lo) { hi = lo + 1; }
    var step = niceStep(hi - lo, Math.max(2, Math.floor(ph / 40)), this.unit);
    lo = Math.floor(lo / step) * step;
    hi = Math.ceil(hi / step) * step;
    if (fx.min !== null) lo = fx.min;
    if (fx.max !== null) hi = fx.max;
    if (hi <= lo) hi = lo + 1;
    if (fx.min !== null || fx.max !== null) step = niceStep(hi - lo, Math.max(2, Math.floor(ph / 40)), this.unit);
    var X = function (t) { return L.l + (t - from) / span * pw; };
    var Y = function (v) { return L.t + ph - (v - lo) / (hi - lo) * ph; };
    this.from = from; this.to = to;

    // grid and axes
    ctx.strokeStyle = line;
    ctx.lineWidth = 1;
    ctx.fillStyle = muted;
    ctx.textAlign = "right";
    ctx.textBaseline = "middle";
    for (var v = lo; v <= hi + step / 2; v += step) {
      var y = Math.round(Y(v)) + 0.5;
      ctx.beginPath(); ctx.moveTo(L.l, y); ctx.lineTo(L.l + pw, y); ctx.stroke();
      ctx.fillText(fmtVal(v, this.unit), L.l - 6, y);
    }
    var ts = timeSteps[timeSteps.length - 1];
    for (var k = 0; k < timeSteps.length; k++) { if (span / timeSteps[k] <= Math.max(2, pw / 90)) { ts = timeSteps[k]; break; } }
    ctx.textAlign = "center";
    ctx.textBaseline = "top";
    var first = Math.ceil((from + zoneOff) / ts) * ts - zoneOff;
    for (var t = first; t <= to; t += ts) {
      var x = Math.round(X(t)) + 0.5;
      ctx.beginPath(); ctx.moveTo(x, L.t); ctx.lineTo(x, L.t + ph); ctx.stroke();
      ctx.fillText(fmtTime(t, span), x, L.t + ph + 6);
    }

    ctx.save();
    ctx.beginPath();
    ctx.rect(L.l, L.t, pw, ph);
    ctx.clip();
    // series: lines (with the min–max band of a single series), areas or
    // bars, stacked when asked (each on top of the ones before)
    var under = function (si, i) { return tops && si > 0 ? tops[si - 1][i] : null; };
    if (kind === "bar") {
      var n = ds[0].t.length || 1, slot = pw / n, bw = Math.max(1, slot * 0.8 / (tops ? 1 : ds.length));
      ds.forEach(function (d, si) {
        var half = d.step > 0 ? d.step / 2 : 0;
        ctx.fillStyle = g.color(si);
        for (var i = 0; i < d.t.length; i++) {
          if (d.avg[i] === null) continue;
          var x = X(d.t[i] + half) - (tops ? bw / 2 : slot * 0.4 - si * bw);
          var top = Y(val(si, i)), base = Y(under(si, i) !== null ? under(si, i) : Math.max(lo, 0));
          ctx.fillRect(x, Math.min(top, base), bw, Math.abs(base - top) || 1);
        }
      });
    } else {
      ds.forEach(function (d, si) {
        var col = g.color(si), segs = segments(d), half = d.step > 0 ? d.step / 2 : 0;
        var band = single && d.min && d.min.length === d.t.length && d.step > 0;
        if (band) {
          ctx.fillStyle = col;
          ctx.globalAlpha = 0.16;
          segs.forEach(function (s) {
            ctx.beginPath();
            s.forEach(function (n, q) { var px = X(d.t[n] + half), py = Y(d.max[n]); if (q) ctx.lineTo(px, py); else ctx.moveTo(px, py); });
            for (var q = s.length - 1; q >= 0; q--) ctx.lineTo(X(d.t[s[q]] + half), Y(d.min[s[q]]));
            ctx.closePath();
            ctx.fill();
          });
          ctx.globalAlpha = 1;
        } else if (kind === "area" || tops || !single) {
          // filled under the line (down to the series below when stacked);
          // a plain line graph of several series gets only a light fill
          ctx.fillStyle = col;
          ctx.globalAlpha = kind === "area" || tops ? 0.28 : 0.08;
          segs.forEach(function (s) {
            ctx.beginPath();
            s.forEach(function (n, q) { var px = X(d.t[n] + half), py = Y(val(si, n)); if (q) ctx.lineTo(px, py); else ctx.moveTo(px, py); });
            for (var q = s.length - 1; q >= 0; q--) {
              var b0 = under(si, s[q]);
              ctx.lineTo(X(d.t[s[q]] + half), Y(b0 !== null ? b0 : Math.max(lo, Math.min(0, hi))));
            }
            ctx.closePath();
            ctx.fill();
          });
          ctx.globalAlpha = 1;
        }
        ctx.strokeStyle = col;
        ctx.lineWidth = 1.6;
        ctx.lineJoin = "round";
        segs.forEach(function (s) {
          ctx.beginPath();
          s.forEach(function (n, q) { var px = X(d.t[n] + half), py = Y(val(si, n)); if (q) ctx.lineTo(px, py); else ctx.moveTo(px, py); });
          if (s.length === 1) { ctx.arc(X(d.t[s[0]] + half), Y(val(si, s[0])), 1.5, 0, 7); }
          ctx.stroke();
        });
      });
    }
    // threshold lines
    [[fx.warn, css("--warn") || "#d97706"], [fx.crit, css("--bad") || "#dc2626"]].forEach(function (th) {
      if (th[0] === null) return;
      var ty = Math.round(Y(th[0])) + 0.5;
      ctx.strokeStyle = th[1];
      ctx.lineWidth = 1.2;
      ctx.setLineDash([6, 4]);
      ctx.beginPath(); ctx.moveTo(L.l, ty); ctx.lineTo(L.l + pw, ty); ctx.stroke();
      ctx.setLineDash([]);
    });
    // drag selection
    if (this.drag && this.drag.x1 !== undefined) {
      ctx.fillStyle = this.color(0);
      ctx.globalAlpha = 0.12;
      ctx.fillRect(Math.min(this.drag.x0, this.drag.x1), L.t, Math.abs(this.drag.x1 - this.drag.x0), ph);
      ctx.globalAlpha = 1;
    }
    // hover
    if (this.hover !== null) {
      var hx = Math.round(X(this.hover)) + 0.5;
      ctx.strokeStyle = muted;
      ctx.lineWidth = 1;
      ctx.beginPath(); ctx.moveTo(hx, L.t); ctx.lineTo(hx, L.t + ph); ctx.stroke();
      ds.forEach(function (d, si) {
        var i = nearest(d, g.hover - (d.step > 0 ? d.step / 2 : 0));
        if (i === null || d.avg[i] === null) return;
        ctx.fillStyle = g.color(si);
        ctx.beginPath(); ctx.arc(X(d.t[i] + (d.step > 0 ? d.step / 2 : 0)), Y(val(si, i)), 3, 0, 7); ctx.fill();
      });
    }
    ctx.restore();
  };

  Graph.prototype.timeAt = function (px) {
    if (!this.L || this.from === undefined) return null;
    var pw = this.L.w - this.L.l - this.L.r;
    return this.from + (px - this.L.l) / pw * (this.to - this.from);
  };

  Graph.prototype.showTip = function (px) {
    var t = this.timeAt(px), g = this;
    if (t === null || !this.data) { this.tip.hidden = true; return; }
    var d0 = this.data[0], i0 = nearest(d0, t - (d0.step > 0 ? d0.step / 2 : 0));
    if (i0 === null) { this.tip.hidden = true; return; }
    this.hover = d0.t[i0] + (d0.step > 0 ? d0.step / 2 : 0);
    var parts = [fmtFull(d0.t[i0])];
    this.data.forEach(function (d, si) {
      var i = nearest(d, t - (d.step > 0 ? d.step / 2 : 0));
      if (i === null) return;
      var label = g.data.length > 1 ? (g.labels[si] || "") + " " : "";
      if (g.data.length === 1 && d.step > 0 && d.min.length) {
        parts.push("avg " + fmtVal(d.avg[i], g.unit), "min " + fmtVal(d.min[i], g.unit), "max " + fmtVal(d.max[i], g.unit));
      } else {
        parts.push(label + fmtVal(d.avg[i], g.unit));
      }
    });
    this.tip.textContent = parts.join(" · ");
    this.tip.hidden = false;
    this.draw();
  };

  Graph.prototype.bind = function () {
    var g = this, c = this.canvas;
    function pos(ev) { var r = c.getBoundingClientRect(); return (ev.touches ? ev.touches[0].clientX : ev.clientX) - r.left; }
    c.addEventListener("mousedown", function (ev) { g.drag = { x0: pos(ev) }; });
    c.addEventListener("mousemove", function (ev) {
      var x = pos(ev);
      if (g.drag) { g.drag.x1 = x; }
      g.showTip(x);
    });
    c.addEventListener("mouseleave", function () { g.hover = null; g.tip.hidden = true; g.drag = null; g.draw(); });
    c.addEventListener("mouseup", function (ev) {
      var dr = g.drag;
      g.drag = null;
      if (!dr || dr.x1 === undefined || Math.abs(dr.x1 - dr.x0) < 8 || !g.data || !g.L) { g.draw(); return; }
      var a = g.timeAt(Math.min(dr.x0, dr.x1)), b = g.timeAt(Math.max(dr.x0, dr.x1));
      if (b - a < 120) b = a + 120;
      g.zoom = { from: a, to: b };
      g.fig.classList.add("zoomed");
      g.load();
      ev.preventDefault();
    });
    c.addEventListener("dblclick", function () { g.zoom = null; g.fig.classList.remove("zoomed"); g.load(); });
    c.addEventListener("touchstart", function (ev) { g.showTip(pos(ev)); }, { passive: true });
    c.addEventListener("touchmove", function (ev) { g.showTip(pos(ev)); }, { passive: true });
  };

  var graphs = figs.map(function (f) { return new Graph(f); });
  graphs.forEach(function (g) { g.load(); });

  var resizeTimer;
  window.addEventListener("resize", function () {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(function () { graphs.forEach(function (g) { g.applySize(); g.draw(); }); }, 150);
  });
  // Light/dark switch: redraw with the new colours.
  function redrawAll() { graphs.forEach(function (g) { g.draw(); g.fillLegend(); }); }
  new MutationObserver(redrawAll).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
  if (window.matchMedia) {
    var mq = window.matchMedia("(prefers-color-scheme: dark)");
    if (mq.addEventListener) mq.addEventListener("change", redrawAll);
  }
  // New samples every minute (not while zoomed into the past).
  setInterval(function () {
    if (document.hidden) return;
    graphs.forEach(function (g) { if (!g.zoom) g.load(); });
  }, 60000);
})();

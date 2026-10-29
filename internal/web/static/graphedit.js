// WatchZit graph editor of a device page: change, add, size, order and
// remove graphs, like panels of a dashboard. The arrangement is stored per
// device; "Automatic graphs" goes back to one graph per check.
(function () {
  "use strict";
  var data = document.getElementById("graph-edit");
  var bar = document.querySelector("[data-graph-bar]"), start = document.querySelector("[data-graph-edit]");
  if (!data || !bar || !start) return;
  var dev = data.getAttribute("data-device"), st;
  try { st = JSON.parse(data.textContent); } catch (e) { return; }
  var tx = document.getElementById("graph-text").dataset;
  var status = bar.querySelector("[data-graph-status]");
  var grid = document.querySelector(".graphs");
  var key = "wz-graph-edit-" + dev;

  function h(tag, attrs, text) {
    var e = document.createElement(tag);
    for (var k in attrs || {}) e.setAttribute(k, attrs[k]);
    if (text !== undefined) e.textContent = text;
    return e;
  }
  function panelOf(id) { for (var i = 0; i < st.panels.length; i++) if (st.panels[i].id === id) return i; return -1; }

  // save stores the arrangement and shows it (the page draws the graphs)
  function save() {
    status.textContent = tx.saving;
    var body = JSON.stringify({ panels: st.panels, hidden: st.hidden });
    fetch("/devices/" + dev + "/graphs", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: body })
      .then(function (r) { return r.json().then(function (j) { if (!r.ok) throw new Error(j.error || r.status); }); })
      .then(function () { try { sessionStorage.setItem(key, "1"); } catch (e) { /* the editor just closes */ } location.reload(); })
      .catch(function (e) { status.textContent = e.message; });
  }

  // tools on each graph while editing
  function tools() {
    Array.prototype.forEach.call(grid.querySelectorAll("figure.graph[data-panel]"), function (fig) {
      var id = fig.getAttribute("data-panel"), t = h("div", { "class": "graph-tools" });
      var b = function (label, title, fn) {
        var x = h("button", { type: "button", "class": "ghost sm", title: title, "aria-label": title }, label);
        x.addEventListener("click", function () { var i = panelOf(id); if (i >= 0) fn(i); });
        t.appendChild(x);
      };
      b("✎", tx.edit, function (i) { editor(st.panels[i]); });
      b("↔", tx.wider, function (i) { st.panels[i].width = st.panels[i].width === "full" ? "" : "full"; save(); });
      b("↕", tx.taller, function (i) { var p = st.panels[i]; p.height = p.height === "" || !p.height ? "l" : p.height === "l" ? "s" : ""; save(); });
      b("▲", tx.up, function (i) { if (i > 0) { st.panels.splice(i - 1, 0, st.panels.splice(i, 1)[0]); save(); } });
      b("▼", tx.down, function (i) { if (i < st.panels.length - 1) { st.panels.splice(i + 1, 0, st.panels.splice(i, 1)[0]); save(); } });
      b("×", tx.remove, function (i) {
        var gone = st.panels.splice(i, 1)[0];
        gone.series.forEach(function (sid) {
          var shown = st.panels.some(function (p) { return p.series.indexOf(sid) >= 0; });
          if (!shown && st.hidden.indexOf(sid) < 0) st.hidden.push(sid);
        });
        save();
      });
      fig.insertBefore(t, fig.firstChild);
      sizer(fig, id);
    });
  }

  // sizer: drag the lower right corner to set the width in columns and the
  // height in pixels, as on a dashboard
  function sizer(fig, id) {
    var grip = h("span", { "class": "graph-resize", title: tx.resize, "aria-hidden": "true" });
    fig.appendChild(grip);
    grip.addEventListener("pointerdown", function (ev) {
      var i = panelOf(id), canvas = fig.querySelector("canvas");
      if (i < 0 || !canvas) return;
      ev.preventDefault();
      grip.setPointerCapture(ev.pointerId);
      var gs = getComputedStyle(grid), n = (gs.gridTemplateColumns || "").split(" ").filter(Boolean).length || 1;
      var gap = parseFloat(gs.columnGap) || 0, colW = (grid.clientWidth - gap * (n - 1)) / n;
      var x0 = ev.clientX, y0 = ev.clientY, w0 = fig.getBoundingClientRect().width, h0 = canvas.clientHeight;
      var cols = 0, hh = 0;
      var move = function (e) {
        cols = Math.max(1, Math.min(4, n, Math.round((w0 + e.clientX - x0 + gap) / (colW + gap))));
        hh = Math.max(80, Math.min(900, Math.round(h0 + e.clientY - y0)));
        fig.setAttribute("data-cols", String(cols));
        fig.setAttribute("data-h", String(hh));
        fig.classList.remove("w-full", "h-s", "h-l");
        window.dispatchEvent(new Event("resize"));
      };
      var up = function () {
        grip.removeEventListener("pointermove", move);
        grip.removeEventListener("pointerup", up);
        if (!cols) return;
        var p = st.panels[i];
        p.cols = cols; p.h = hh; p.width = ""; p.height = "";
        save();
      };
      grip.addEventListener("pointermove", move);
      grip.addEventListener("pointerup", up);
    });
  }

  // editor: one graph — title, kind, values, axis ends and thresholds
  function editor(p) {
    var isNew = panelOf(p.id) < 0;
    var dlg = h("dialog", { "class": "graph-editor" }), form = h("form", { method: "dialog", "class": "stack" });
    dlg.appendChild(form);
    form.appendChild(h("h3", {}, isNew ? tx["new"] : (p.title || tx.edit)));
    var field = function (label, input) { var l = h("label", {}, label); l.appendChild(input); form.appendChild(l); return input; };
    var title = field(tx.title, h("input", { value: p.title || "", maxlength: "100" }));
    var kind = field(tx.kind, h("select"));
    [["", tx.line], ["area", tx.area], ["bar", tx.bar]].forEach(function (o) { var op = h("option", { value: o[0] }, o[1]); if ((p.type || "") === o[0]) op.selected = true; kind.appendChild(op); });
    var stackL = h("label", { "class": "check" }), stack = h("input", { type: "checkbox" });
    stack.checked = !!p.stack;
    stackL.appendChild(stack); stackL.appendChild(document.createTextNode(" " + tx.stack));
    form.appendChild(stackL);
    var box = h("fieldset", { "class": "box graph-series" });
    box.appendChild(h("legend", {}, tx.values));
    var checks = [], colors = {};
    p.colors = p.colors || {};
    st.series.forEach(function (s) {
      var l = h("label", { "class": "check" }), c = h("input", { type: "checkbox", value: String(s.id) });
      c.checked = p.series.indexOf(s.id) >= 0;
      l.appendChild(c); l.appendChild(document.createTextNode(" " + s.label + (s.unit ? " (" + s.unit + ")" : "")));
      var col = h("input", { type: "color", "class": "graph-color", title: tx.color, "aria-label": tx.color, value: p.colors[String(s.id)] || "#4f46e5" });
      col.addEventListener("input", function () { colors[String(s.id)] = col.value; });
      if (p.colors[String(s.id)]) colors[String(s.id)] = p.colors[String(s.id)];
      l.appendChild(col);
      box.appendChild(l); checks.push(c);
    });
    form.appendChild(box);
    var qbox = queriesBox(p, form);
    var nums = h("div", { "class": "cols" });
    form.appendChild(nums);
    var numField = function (label, v) {
      var l = h("label", {}, label), i = h("input", { type: "number", step: "any", value: v === undefined || v === null ? "" : String(v), placeholder: tx.auto });
      l.appendChild(i); nums.appendChild(l); return i;
    };
    var min = numField(tx.min, p.min), max = numField(tx.max, p.max), warn = numField(tx.warn, p.warn), crit = numField(tx.crit, p.crit);
    var err = h("p", { "class": "small warntext", hidden: "" });
    form.appendChild(err);
    var row = h("div", { "class": "row" });
    var ok = h("button", { type: "button", "class": "btn primary" }, tx.save), no = h("button", { type: "button", "class": "btn secondary" }, tx.cancel);
    row.appendChild(ok); row.appendChild(no);
    form.appendChild(row);
    var numOf = function (i) { return i.value === "" || isNaN(+i.value) ? undefined : +i.value; };
    ok.addEventListener("click", function () {
      var series = checks.filter(function (c) { return c.checked; }).map(function (c) { return +c.value; });
      var queries = qbox.read();
      if (!series.length && !queries.length) { err.textContent = tx.pick; err.hidden = false; return; }
      p.queries = queries;
      p.title = title.value.trim(); p.type = kind.value; p.stack = stack.checked; p.series = series;
      p.min = numOf(min); p.max = numOf(max); p.warn = numOf(warn); p.crit = numOf(crit);
      p.colors = {};
      series.forEach(function (sid) { if (colors[String(sid)]) p.colors[String(sid)] = colors[String(sid)]; });
      queries.forEach(function (q, i) { if (q.color) p.colors["q" + i] = q.color; delete q.color; });
      st.hidden = st.hidden.filter(function (sid) { return series.indexOf(sid) < 0; });
      if (isNew) st.panels.push(p);
      dlg.close();
      save();
    });
    no.addEventListener("click", function () { dlg.close(); });
    dlg.addEventListener("close", function () { dlg.remove(); });
    document.body.appendChild(dlg);
    dlg.showModal();
  }

  // queriesBox: the queries of data sources a graph shows, each with a
  // button that tries it
  function queriesBox(p, form) {
    var box = h("fieldset", { "class": "box graph-queries" }), rows = [];
    box.appendChild(h("legend", {}, tx.queries));
    form.appendChild(box);
    var list = h("div", { "class": "stack" });
    box.appendChild(list);
    var srcs = st.sources || [];
    if (!srcs.length) box.appendChild(h("p", { "class": "small muted" }, tx.nosources));
    var add = function (q, color) {
      var row = h("div", { "class": "graph-query stack" });
      var src = h("select", { "aria-label": tx.source });
      srcs.forEach(function (s) { var o = h("option", { value: String(s.id) }, s.name); if (q.source === s.id) o.selected = true; src.appendChild(o); });
      var text = h("textarea", { rows: "4", maxlength: "8000", "class": "mono", "aria-label": tx.query, placeholder: tx.query });
      text.value = q.query || "";
      var label = h("input", { maxlength: "100", placeholder: tx.seriesname, "aria-label": tx.seriesname, value: q.label || "" });
      var unit = h("input", { maxlength: "20", placeholder: tx.unit, "aria-label": tx.unit, value: q.unit || "", "class": "w-sm" });
      var col = h("input", { type: "color", "class": "graph-color", title: tx.color, "aria-label": tx.color, value: color || "#4f46e5" });
      var picked = color || "";
      col.addEventListener("input", function () { picked = col.value; });
      var top = h("div", { "class": "row" });
      var tryB = h("button", { type: "button", "class": "btn secondary sm" }, tx.tryq);
      var del = h("button", { type: "button", "class": "ghost sm", title: tx.remove, "aria-label": tx.remove }, "×");
      [src, label, unit, col, tryB, del].forEach(function (e) { top.appendChild(e); });
      var out = h("p", { "class": "small muted graph-try", hidden: "" });
      row.appendChild(top); row.appendChild(text); row.appendChild(out);
      list.appendChild(row);
      var r = { read: function () { return { source: +src.value, query: text.value.trim(), label: label.value.trim(), unit: unit.value.trim(), color: picked }; } };
      rows.push(r);
      del.addEventListener("click", function () { rows.splice(rows.indexOf(r), 1); row.remove(); });
      tryB.addEventListener("click", function () {
        var fig = document.querySelector("figure.graph[data-range]"), rg = fig ? fig.getAttribute("data-range") : "24h";
        out.hidden = false; out.className = "small muted graph-try"; out.textContent = tx.running;
        var q = r.read(); delete q.color;
        fetch("/datasources/try?range=" + encodeURIComponent(rg), { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(q) })
          .then(function (x) { return x.json(); })
          .then(function (j) {
            if (j.error) { out.className = "small warntext graph-try"; out.textContent = j.error; return; }
            out.textContent = j.series.length ? "✓ " + j.series.map(function (s) { return s.label + " (" + s.points + ")"; }).join(", ") : tx.noseries;
          })
          .catch(function (e) { out.className = "small warntext graph-try"; out.textContent = e.message; });
      });
    };
    (p.queries || []).forEach(function (q, i) { add(q, (p.colors || {})["q" + i]); });
    if (srcs.length) {
      var more = h("button", { type: "button", "class": "btn secondary sm" }, "+ " + tx.addquery);
      more.addEventListener("click", function () { if (rows.length < 6) add({ source: srcs[0].id }); });
      box.appendChild(more);
    }
    return { read: function () { return rows.map(function (r) { return r.read(); }).filter(function (q) { return q.source; }); } };
  }

  function begin() {
    start.hidden = true;
    bar.hidden = false;
    grid.classList.add("editing");
    tools();
  }
  start.addEventListener("click", begin);
  bar.querySelector("[data-graph-done]").addEventListener("click", function () {
    try { sessionStorage.removeItem(key); } catch (e) { /* nothing to forget */ }
    location.reload();
  });
  bar.querySelector("[data-graph-add]").addEventListener("click", function () {
    editor({ id: "p" + Date.now().toString(36), title: "", series: [], type: "" });
  });
  var reset = bar.querySelector("[data-graph-reset]");
  reset.addEventListener("click", function () {
    if (!window.confirm(reset.getAttribute("data-confirm-text"))) return;
    fetch("/devices/" + dev + "/graphs?reset=1", { method: "POST", credentials: "same-origin" }).then(function () { location.reload(); });
  });
  // after a save the page comes back with the editor open
  try { if (sessionStorage.getItem(key)) begin(); } catch (e) { /* stays closed */ }
})();

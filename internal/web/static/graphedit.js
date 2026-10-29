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
    var checks = [];
    st.series.forEach(function (s) {
      var l = h("label", { "class": "check" }), c = h("input", { type: "checkbox", value: String(s.id) });
      c.checked = p.series.indexOf(s.id) >= 0;
      l.appendChild(c); l.appendChild(document.createTextNode(" " + s.label + (s.unit ? " (" + s.unit + ")" : "")));
      box.appendChild(l); checks.push(c);
    });
    form.appendChild(box);
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
      if (!series.length) { err.textContent = tx.pick; err.hidden = false; return; }
      p.title = title.value.trim(); p.type = kind.value; p.stack = stack.checked; p.series = series;
      p.min = numOf(min); p.max = numOf(max); p.warn = numOf(warn); p.crit = numOf(crit);
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

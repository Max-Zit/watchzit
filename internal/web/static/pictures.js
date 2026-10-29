// WatchZit picture library: the list of built-in pictures to copy, and the
// editor that marks the ports of a picture (positions in picture
// coordinates, port 1 first).
(function () {
  "use strict";
  var v = (document.currentScript && document.currentScript.src.split("v=")[1]) || "";

  function h(tag, attrs, text) {
    var e = document.createElement(tag);
    for (var k in attrs || {}) e.setAttribute(k, attrs[k]);
    if (text !== undefined) e.textContent = text;
    return e;
  }

  // ---- built-in pictures: search, filter, copy
  var lib = document.querySelector("[data-lib]");
  if (lib) {
    var q = lib.querySelector("[data-lib-q]"), vend = lib.querySelector("[data-lib-vendor]"), type = lib.querySelector("[data-lib-type]");
    var grid = document.querySelector("[data-lib-grid]"), list = [];
    var replaced = (grid.getAttribute("data-replaced") || "").split(",");
    var editIcon = document.querySelector("template[data-icon-edit]").innerHTML, copyIcon = document.querySelector("template[data-icon-copy]").innerHTML;
    var typeOf = function (i) {
      if (i.type) return i.type;
      if (i.kind && i.kind !== "other") return i.kind;
      return ["rack", "logical", "power"].indexOf(i.cat) >= 0 ? i.cat : "other";
    };
    var fill = function () {
      grid.innerHTML = "";
      var f = q.value.toLowerCase().trim().split(/\s+/).filter(Boolean), n = 0, max = 120;
      list.forEach(function (i) {
        if (vend.value && (i.vendor || "") !== vend.value) return;
        if (type.value && typeOf(i) !== type.value) return;
        if (f.some(function (w) { return i.label.toLowerCase().indexOf(w) < 0; })) return;
        if (++n > max) return;
        // pencil: change the built-in picture itself; copy: a new picture
        var form = h("form", { method: "post", action: "/pictures/copy", "class": "libitem" });
        form.appendChild(h("input", { type: "hidden", name: "name", value: i.name }));
        form.appendChild(h("img", { src: "/static/devices/" + i.name + ".svg?v=" + v, alt: "", loading: "lazy" }));
        form.appendChild(h("small", {}, i.label));
        var acts = h("div", { "class": "row" });
        if (replaced.indexOf(i.name) >= 0) acts.appendChild(h("span", { "class": "badge" }, grid.getAttribute("data-changed-text")));
        [["edit", "data-edit-text", editIcon], ["copy", "data-copy-text", copyIcon]].forEach(function (b) {
          var btn = h("button", { "class": "ghost sm", name: "mode", value: b[0], title: grid.getAttribute(b[1]), "aria-label": grid.getAttribute(b[1]) });
          btn.innerHTML = b[2];
          acts.appendChild(btn);
        });
        form.appendChild(acts);
        grid.appendChild(form);
      });
      if (n > max) grid.appendChild(h("p", { "class": "muted small" }, grid.getAttribute("data-more-text") + " (" + n + ")"));
    };
    fetch("/static/devices/index.json?v=" + v, { credentials: "same-origin" }).then(function (r) { return r.json(); }).then(function (l) {
      list = l || [];
      var vs = [];
      list.forEach(function (i) { if (i.vendor && vs.indexOf(i.vendor) < 0) vs.push(i.vendor); });
      vs.sort().forEach(function (x) { vend.appendChild(h("option", { value: x }, x)); });
      fill();
    });
    [q, vend, type].forEach(function (e) { e.addEventListener(e === q ? "input" : "change", fill); });
  }

  // ---- the port editor
  var form = document.querySelector("[data-piceditor]");
  if (!form) return;
  var canvas = form.querySelector("[data-canvas]"), img = canvas.querySelector("img");
  var input = form.querySelector("[data-ports]"), fiber = form.querySelector("[data-fiber]");
  var count = form.querySelector("[data-portcount]"), hint = form.querySelector("[data-hint]");
  var ports = [];
  try { ports = JSON.parse(input.value) || []; } catch (e) { ports = []; }
  var W = +canvas.getAttribute("data-w"), H = +canvas.getAttribute("data-h");
  var row = null; // adding a row: {first: [x, y]} after the first click

  function size() { return { w: W || img.naturalWidth || 100, h: H || img.naturalHeight || 100 }; }
  function scale() { return img.clientWidth / size().w; }
  function toPic(ev) {
    var r = img.getBoundingClientRect(), s = scale(), sz = size();
    var x = Math.max(0, Math.min(sz.w, (ev.clientX - r.left) / s)), y = Math.max(0, Math.min(sz.h, (ev.clientY - r.top) / s));
    return [Math.round(x * 10) / 10, Math.round(y * 10) / 10];
  }
  function draw() {
    Array.prototype.slice.call(canvas.querySelectorAll(".pdot")).forEach(function (d) { d.remove(); });
    var s = scale(), nf = Math.min(+fiber.value || 0, ports.length);
    var dotSize = Math.max(14, Math.min(26, 14 * s * 1.4));
    ports.forEach(function (p, i) {
      var d = h("span", { "class": "pdot" + (i >= ports.length - nf ? " sfp" : ""), "data-i": i, title: String(i + 1) }, String(i + 1));
      d.style.left = (p[0] * s) + "px";
      d.style.top = (p[1] * s) + "px";
      d.style.width = d.style.height = dotSize + "px";
      d.style.fontSize = Math.max(8, dotSize * 0.5) + "px";
      canvas.appendChild(d);
    });
    input.value = JSON.stringify(ports);
    count.textContent = "(" + ports.length + ")";
  }
  function rowPorts(a, b, n, lines) {
    var out = [];
    if (lines === 2) {
      var cols = Math.ceil(n / 2);
      for (var i = 0; i < n; i++) {
        var c = Math.floor(i / 2), t = cols > 1 ? c / (cols - 1) : 0;
        out.push([a[0] + (b[0] - a[0]) * t, i % 2 === 0 ? a[1] : b[1]]);
      }
    } else {
      for (var j = 0; j < n; j++) {
        var u = n > 1 ? j / (n - 1) : 0;
        out.push([a[0] + (b[0] - a[0]) * u, a[1] + (b[1] - a[1]) * u]);
      }
    }
    return out.map(function (p) { return [Math.round(p[0] * 10) / 10, Math.round(p[1] * 10) / 10]; });
  }
  function setHint(t) { hint.textContent = t || ""; hint.hidden = !t; }

  var drag = null, moved = false;
  canvas.addEventListener("pointerdown", function (ev) {
    var d = ev.target.closest(".pdot");
    if (d) { drag = +d.getAttribute("data-i"); moved = false; d.setPointerCapture(ev.pointerId); ev.preventDefault(); }
  });
  canvas.addEventListener("pointermove", function (ev) {
    if (drag === null) return;
    ports[drag] = toPic(ev); moved = true; draw();
    var d = canvas.querySelector('.pdot[data-i="' + drag + '"]');
    if (d) d.setPointerCapture(ev.pointerId);
  });
  canvas.addEventListener("pointerup", function () { drag = null; });
  canvas.addEventListener("dblclick", function (ev) {
    var d = ev.target.closest(".pdot");
    if (!d) return;
    ports.splice(+d.getAttribute("data-i"), 1);
    draw();
  });
  img.addEventListener("click", function (ev) {
    if (moved) { moved = false; return; }
    var p = toPic(ev);
    if (row) {
      if (!row.first) { row.first = p; setHint(hint.getAttribute("data-last")); return; }
      var n = Math.max(2, Math.min(96, +form.querySelector("[data-row-n]").value || 2));
      ports = ports.concat(rowPorts(row.first, p, n, +form.querySelector("[data-row-lines]").value));
      row = null; setHint(""); draw();
      return;
    }
    if (ports.length < 512) { ports.push(p); draw(); }
  });
  form.querySelector("[data-row]").addEventListener("click", function () { row = {}; setHint(hint.getAttribute("data-first")); });
  form.querySelector("[data-undo]").addEventListener("click", function () { ports.pop(); draw(); });
  var clear = form.querySelector("[data-clear]");
  clear.addEventListener("click", function () {
    if (ports.length && window.confirm(clear.getAttribute("data-confirm-text"))) { ports = []; draw(); }
  });
  fiber.addEventListener("input", draw);
  window.addEventListener("resize", draw);
  if (img.complete) draw(); else img.addEventListener("load", draw);
})();

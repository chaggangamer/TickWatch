(function () {
  "use strict";

  var container = document.getElementById("chart");
  if (!container) return;
  var monitorID = container.getAttribute("data-monitor");

  fetch("/dash/monitors/" + monitorID + "/series?hours=24")
    .then(function (res) { return res.ok ? res.json() : null; })
    .then(function (data) {
      if (!data) return;
      draw(data.points || []);
    })
    .catch(function () {});

  function draw(points) {
    var note = document.getElementById("chart-note");
    if (!points.length) {
      container.innerHTML = "<p class='muted'>No successful checks in the last 24 hours.</p>";
      return;
    }
    var w = 800;
    var h = 240;
    var pad = { left: 46, right: 12, top: 14, bottom: 26 };
    var minMs = points[0].ms;
    var maxMs = points[0].ms;
    for (var i = 1; i < points.length; i++) {
      if (points[i].ms < minMs) minMs = points[i].ms;
      if (points[i].ms > maxMs) maxMs = points[i].ms;
    }
    if (maxMs - minMs < 10) maxMs = minMs + 10;
    var tMin = points[0].t;
    var tMax = points[points.length - 1].t;
    if (tMax === tMin) tMax = tMin + 1;

    function x(t) { return pad.left + ((t - tMin) / (tMax - tMin)) * (w - pad.left - pad.right); }
    function y(ms) { return pad.top + (1 - (ms - minMs) / (maxMs - minMs)) * (h - pad.top - pad.bottom); }

    var parts = [];
    parts.push("<svg viewBox='0 0 " + w + " " + h + "' preserveAspectRatio='none' role='img'>");
    for (var g = 0; g <= 4; g++) {
      var gy = pad.top + (g / 4) * (h - pad.top - pad.bottom);
      var gval = Math.round(maxMs - (g / 4) * (maxMs - minMs));
      parts.push("<line x1='" + pad.left + "' y1='" + gy + "' x2='" + (w - pad.right) + "' y2='" + gy + "' class='grid'/>");
      parts.push("<text x='" + (pad.left - 6) + "' y='" + (gy + 4) + "' text-anchor='end' class='label'>" + gval + "</text>");
    }
    var path = "";
    points.forEach(function (p, i) {
      path += (i === 0 ? "M" : "L") + x(p.t).toFixed(1) + "," + y(p.ms).toFixed(1);
      parts.push("<circle cx='" + x(p.t).toFixed(1) + "' cy='" + y(p.ms).toFixed(1) + "' r='2.5'>" +
        "<title>" + new Date(p.t * 1000).toLocaleString() + " - " + p.ms + " ms</title></circle>");
    });
    parts.push("<path d='" + path + "' class='line'/>");
    parts.push("<text x='" + pad.left + "' y='" + (h - 8) + "' class='label'>" + new Date(tMin * 1000).toLocaleString() + "</text>");
    parts.push("<text x='" + (w - pad.right) + "' y='" + (h - 8) + "' text-anchor='end' class='label'>" + new Date(tMax * 1000).toLocaleString() + "</text>");
    parts.push("</svg>");
    container.innerHTML = parts.join("");
    if (note) {
      var last = points[points.length - 1];
      note.textContent = "Last: " + last.ms + " ms, min: " + minMs + " ms, max: " + maxMs + " ms, " + points.length + " points";
    }
  }
})();

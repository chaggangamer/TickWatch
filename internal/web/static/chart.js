(function () {
  "use strict";

  var container = document.getElementById("chart");
  if (!container) return;
  var monitorID = container.getAttribute("data-monitor");

  container.classList.add("chart-box");
  var tooltip = document.createElement("div");
  tooltip.className = "chart-tooltip";
  container.appendChild(tooltip);

  fetch("/dash/monitors/" + monitorID + "/series?hours=24")
    .then(function (res) { return res.ok ? res.json() : null; })
    .then(function (data) {
      if (!data) return;
      draw(data.points || []);
    })
    .catch(function () {});

  function fmtTime(t) {
    var d = new Date(t * 1000);
    var hh = String(d.getHours()).padStart(2, "0");
    var mm = String(d.getMinutes()).padStart(2, "0");
    return hh + ":" + mm;
  }

  function draw(points) {
    var note = document.getElementById("chart-note");
    if (!points.length) {
      container.innerHTML = "<p class='muted'>No successful checks in the last 24 hours. The chart appears once checks start coming in.</p>";
      return;
    }
    var w = 800;
    var h = 260;
    var pad = { left: 54, right: 14, top: 18, bottom: 30 };
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
    parts.push("<svg viewBox='0 0 " + w + " " + h + "' preserveAspectRatio='none' role='img' aria-label='Response time chart'>");
    for (var g = 0; g <= 4; g++) {
      var gy = pad.top + (g / 4) * (h - pad.top - pad.bottom);
      var gval = Math.round(maxMs - (g / 4) * (maxMs - minMs));
      parts.push("<line x1='" + pad.left + "' y1='" + gy + "' x2='" + (w - pad.right) + "' y2='" + gy + "' class='grid'/>");
      parts.push("<text x='" + (pad.left - 8) + "' y='" + (gy + 4) + "' text-anchor='end' class='label'>" + gval + "</text>");
    }
    parts.push("<text x='" + (pad.left - 8) + "' y='" + (pad.top - 6) + "' text-anchor='end' class='label'>ms</text>");
    for (var tick = 0; tick <= 3; tick++) {
      var tt = tMin + (tick / 3) * (tMax - tMin);
      var tx = x(tt);
      var anchor = "middle";
      if (tick === 0) anchor = "start";
      if (tick === 3) anchor = "end";
      parts.push("<text x='" + tx + "' y='" + (h - 8) + "' text-anchor='" + anchor + "' class='label'>" + fmtTime(tt) + "</text>");
    }
    var path = "";
    points.forEach(function (p, i) {
      path += (i === 0 ? "M" : "L") + x(p.t).toFixed(1) + "," + y(p.ms).toFixed(1);
    });
    parts.push("<path d='" + path + "' class='line'/>");
    parts.push("<line class='crosshair' id='chart-crosshair' x1='0' y1='" + pad.top + "' x2='0' y2='" + (h - pad.bottom) + "' visibility='hidden'/>");
    points.forEach(function (p, i) {
      parts.push("<circle cx='" + x(p.t).toFixed(1) + "' cy='" + y(p.ms).toFixed(1) + "' r='2.5' data-idx='" + i + "' class='point'/>");
    });
    parts.push("</svg>");
    container.innerHTML = tooltip.outerHTML + parts.join("");
    tooltip = container.querySelector(".chart-tooltip");
    var svg = container.querySelector("svg");
    var crosshair = container.querySelector("#chart-crosshair");

    svg.addEventListener("mousemove", function (event) {
      var rect = svg.getBoundingClientRect();
      var relX = (event.clientX - rect.left) / rect.width * w;
      var best = -1;
      var bestDist = Infinity;
      for (var i = 0; i < points.length; i++) {
        var d = Math.abs(x(points[i].t) - relX);
        if (d < bestDist) {
          bestDist = d;
          best = i;
        }
      }
      if (best < 0) return;
      var p = points[best];
      crosshair.setAttribute("x1", x(p.t).toFixed(1));
      crosshair.setAttribute("x2", x(p.t).toFixed(1));
      crosshair.setAttribute("visibility", "visible");
      tooltip.style.display = "block";
      tooltip.textContent = fmtTime(p.t) + " - " + p.ms + " ms";
      var px = x(p.t) / w * rect.width;
      var py = y(p.ms) / h * rect.height;
      var flip = px > rect.width / 2;
      tooltip.style.left = (px + (flip ? -12 : 12)) + "px";
      tooltip.style.top = Math.max(0, py - 34) + "px";
      tooltip.style.transform = "translateX(" + (flip ? "-100%" : "0") + ")";
    });
    svg.addEventListener("mouseleave", function () {
      crosshair.setAttribute("visibility", "hidden");
      tooltip.style.display = "none";
    });

    if (note) {
      var last = points[points.length - 1];
      note.textContent = "Last: " + last.ms + " ms, min: " + minMs + " ms, max: " + maxMs + " ms, " + points.length + " points";
    }
  }
})();

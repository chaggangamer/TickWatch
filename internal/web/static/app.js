(function () {
  "use strict";

  function initTypeToggle() {
    var select = document.getElementById("type-select") ||
      document.getElementById("channel-type") ||
      document.querySelector("select[name='type']");
    if (!select) return;
    var update = function () {
      var value = select.value;
      document.querySelectorAll(".type-fieldset").forEach(function (fs) {
        fs.classList.toggle("active", fs.getAttribute("data-type") === value);
      });
    };
    select.addEventListener("change", update);
    update();
  }

  function initConfirmDelete() {
    document.querySelectorAll("form.confirm-delete").forEach(function (form) {
      form.addEventListener("submit", function (event) {
        if (!window.confirm("Delete this item? This cannot be undone.")) {
          event.preventDefault();
        }
      });
    });
  }

  function initChannelTest() {
    document.querySelectorAll(".test-channel").forEach(function (button) {
      button.addEventListener("click", function () {
        var result = document.getElementById("test-result");
        button.disabled = true;
        fetch("/channels/" + button.getAttribute("data-id") + "/test", {
          method: "POST",
          headers: { "X-CSRF-Token": button.getAttribute("data-csrf") }
        }).then(function (res) {
          return res.json().then(function (body) { return { ok: res.ok, body: body }; });
        }).then(function (data) {
          button.disabled = false;
          if (!result) return;
          if (data.ok) {
            result.textContent = "Test notification sent.";
            result.className = "test-result ok";
          } else {
            var msg = data.body && data.body.error ? data.body.error.message : "send failed";
            result.textContent = "Test failed: " + msg;
            result.className = "test-result fail";
          }
        }).catch(function () {
          button.disabled = false;
          if (result) {
            result.textContent = "Test failed: network error";
            result.className = "test-result fail";
          }
        });
      });
    });
  }

  function initLiveState() {
    var table = document.getElementById("monitor-table");
    if (!table) return;
    var refresh = function () {
      fetch("/dash/state").then(function (res) { return res.ok ? res.json() : null; }).then(function (data) {
        if (!data || !data.items) return;
        data.items.forEach(function (item) {
          var row = table.querySelector('tr[data-monitor-id="' + item.id + '"]');
          if (!row) return;
          var dot = row.querySelector(".dot");
          if (dot) {
            var state = item.enabled ? item.status : "paused";
            dot.className = "dot dot-" + state;
            dot.title = state;
          }
          var latency = row.querySelector(".latency");
          if (latency) {
            latency.textContent = item.latency_ms ? item.latency_ms + " ms" : "-";
          }
          var uptime = row.querySelector(".uptime");
          if (uptime) {
            uptime.textContent = typeof item.uptime_24h === "number" ? item.uptime_24h.toFixed(2) + "%" : "-";
          }
          var lastCheck = row.querySelector(".last-check");
          if (lastCheck && item.next_deadline_at) {
            lastCheck.textContent = "deadline " + new Date(item.next_deadline_at * 1000).toLocaleTimeString();
          }
        });
      }).catch(function () {});
    };
    setInterval(refresh, 5000);
  }

  document.addEventListener("DOMContentLoaded", function () {
    initTypeToggle();
    initConfirmDelete();
    initChannelTest();
    initLiveState();
  });
})();

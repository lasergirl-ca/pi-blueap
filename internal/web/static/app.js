// palm-gw web UI: pairing-window countdowns and the live event feed.
// No inline script anywhere (the CSP is script-src 'self').
(function () {
  "use strict";

  function pad(n) { return (n < 10 ? "0" : "") + n; }

  // <span data-expires="RFC 3339">: mm:ss left; reload when it reaches zero.
  function tick() {
    document.querySelectorAll("[data-expires]").forEach(function (el) {
      var left = Math.round((Date.parse(el.dataset.expires) - Date.now()) / 1000);
      if (left <= 0) {
        el.textContent = "0:00";
        if (!el.dataset.reloaded) { el.dataset.reloaded = "1"; setTimeout(function () { location.reload(); }, 1500); }
        return;
      }
      el.textContent = Math.floor(left / 60) + ":" + pad(left % 60);
    });
  }
  setInterval(tick, 1000);
  document.addEventListener("DOMContentLoaded", tick);
  document.addEventListener("htmx:afterSwap", tick);

  // <ul id="feed" data-src="/events?...">: the daemon's events, newest first.
  function feed() {
    var ul = document.getElementById("feed");
    if (!ul || !window.EventSource) return;
    var es = new EventSource(ul.dataset.src);
    var first = true;
    es.onmessage = function (m) {
      var e;
      try { e = JSON.parse(m.data); } catch (_) { return; }
      if (first) { ul.textContent = ""; first = false; }
      var li = document.createElement("li");
      var t = document.createElement("span");
      t.className = "muted";
      t.textContent = new Date(e.time).toLocaleTimeString() + " ";
      li.appendChild(t);
      var text = e.event;
      if (e.name) text += " “" + e.name + "”";
      if (e.addr) text += " " + e.addr;
      for (var k in (e.fields || {})) text += " " + k + "=" + e.fields[k];
      li.appendChild(document.createTextNode(text));
      ul.insertBefore(li, ul.firstChild);
      while (ul.children.length > 50) ul.removeChild(ul.lastChild);
      // The pairing page shows the window's state: reload when it changes,
      // but not for the past events that arrive first.
      var changes = e.event === "window_open" || e.event === "window_closed" || e.event === "paired";
      if (changes && ul.dataset.ready && document.querySelector(".pairing, form[action='/pair']")) {
        setTimeout(function () { location.reload(); }, 800);
      }
    };
    es.onopen = function () { setTimeout(function () { ul.dataset.ready = "1"; }, 1500); };
    es.onerror = function () { if (first) ul.innerHTML = '<li class="muted">Not connected; retrying…</li>'; };
  }
  document.addEventListener("DOMContentLoaded", feed);
})();

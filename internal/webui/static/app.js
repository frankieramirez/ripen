(function () {
  "use strict";

  var table = document.getElementById("services");
  if (!table) {
    return;
  }
  var body = table.querySelector("tbody");
  var updated = document.getElementById("last-updated");

  function text(node, value) {
    node.textContent = value;
    return node;
  }

  function shortDigest(digest) {
    if (!digest) {
      return "—";
    }
    return digest.replace(/^sha256:/, "").slice(0, 12);
  }

  function cell(row, value, className) {
    var td = document.createElement("td");
    if (className) {
      td.className = className;
    }
    text(td, value);
    row.appendChild(td);
    return td;
  }

  function render(services) {
    body.replaceChildren();
    services.forEach(function (service) {
      var row = document.createElement("tr");
      cell(row, service.backend);
      cell(row, service.stack);
      cell(row, service.service || "—", service.service ? "" : "muted");

      var baseline = document.createElement("td");
      var code = document.createElement("code");
      text(code, shortDigest(service.baseline));
      baseline.appendChild(code);
      row.appendChild(baseline);

      var candidate = document.createElement("td");
      if (service.candidate) {
        var digest = document.createElement("code");
        text(digest, shortDigest(service.candidate.digest));
        candidate.appendChild(digest);
        var badge = document.createElement("span");
        badge.className = "badge " + (service.candidate.mature ? "ready" : "waiting");
        text(badge, service.candidate.mature ? "mature" : "maturing");
        candidate.appendChild(document.createTextNode(" "));
        candidate.appendChild(badge);
      } else {
        candidate.className = "muted";
        text(candidate, "none");
      }
      row.appendChild(candidate);

      var last = document.createElement("td");
      if (service.last_result) {
        var result = document.createElement("span");
        result.className = "result " + service.last_result.result;
        text(result, service.last_result.result);
        last.appendChild(result);
      } else {
        last.className = "muted";
        text(last, "—");
      }
      row.appendChild(last);

      body.appendChild(row);
    });
  }

  function poll() {
    fetch("/api/status", { headers: { Accept: "application/json" } })
      .then(function (answer) {
        if (!answer.ok) {
          throw new Error("status " + answer.status);
        }
        return answer.json();
      })
      .then(function (envelope) {
        if (!envelope.ok) {
          return;
        }
        render(envelope.data.services);
        if (updated) {
          text(updated, "Last read " + new Date().toLocaleTimeString() + ".");
        }
        var banner = document.getElementById("breaker");
        if (banner && !envelope.data.breaker.open) {
          banner.remove();
        } else if (!banner && envelope.data.breaker.open) {
          window.location.reload();
        }
      })
      .catch(function () {
        if (updated) {
          text(updated, "The daemon did not answer the last read.");
        }
      });
  }

  setInterval(poll, 15000);
})();

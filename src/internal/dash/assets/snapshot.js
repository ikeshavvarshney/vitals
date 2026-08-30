// Receiver for the bookmarklet.
//
// The bookmarklet cannot post to this server from the page it measured: Chrome
// gates requests from a public page to a loopback address behind a permission
// the page will not have. A top-level navigation is not subject to that gate,
// so the bookmarklet navigates here with the payload in the URL fragment, and
// this page performs the same-origin POST that the page could not.
//
// The fragment is never sent to a server by any browser, so the measurement
// does not appear in an access log on its way here.
(function () {
  var status = document.getElementById('snap-status');
  var table = document.getElementById('snap-table');
  var open = document.getElementById('snap-open');

  var LABELS = {
    lcp: 'Largest Contentful Paint',
    inp: 'Interaction to Next Paint',
    cls: 'Cumulative Layout Shift',
    fcp: 'First Contentful Paint',
    ttfb: 'Time to First Byte'
  };
  var ORDER = ['lcp', 'inp', 'cls', 'fcp', 'ttfb'];

  function fail(message) {
    status.textContent = message;
    status.setAttribute('data-state', 'error');
    // The dashboard is still worth opening after a failure; it simply will not
    // hold this measurement.
    settleLink('Open the dashboard');
  }

  // settleLink makes the dashboard link usable. Until the POST has finished the
  // link is inert: following it unloads this page, and an unload cancels a
  // request that is still in flight, which loses the measurement the visitor
  // just took. That was the bug this guard exists for.
  function settleLink(label) {
    if (!open) return;
    open.textContent = label;
    open.classList.remove('is-pending');
    open.removeAttribute('aria-disabled');
  }

  function readPayload() {
    var raw = location.hash.slice(1);
    if (!raw) return null;
    try {
      return JSON.parse(decodeURIComponent(raw));
    } catch (e) {
      return null;
    }
  }

  function renderTable(payload) {
    var head = document.createElement('thead');
    head.innerHTML = '<tr><th scope="col">Metric</th><th scope="col">Value</th></tr>';
    var body = document.createElement('tbody');

    ORDER.forEach(function (key) {
      var v = payload.m[key];
      if (v === undefined) return;
      var tr = document.createElement('tr');
      var th = document.createElement('th');
      th.scope = 'row';
      th.textContent = LABELS[key] || key;
      var td = document.createElement('td');
      td.className = 'num';
      td.textContent = key === 'cls' ? v.toFixed(3) : Math.round(v) + 'ms';
      tr.appendChild(th);
      tr.appendChild(td);
      body.appendChild(tr);
    });

    var caption = table.querySelector('caption');
    table.textContent = '';
    if (caption) table.appendChild(caption);
    table.appendChild(head);
    table.appendChild(body);
  }

  var payload = readPayload();
  if (!payload || typeof payload.u !== 'string' || !payload.m) {
    fail('No snapshot in this URL. Use the bookmarklet on the page you want to measure.');
    return;
  }

  renderTable(payload);

  if (open) {
    open.addEventListener('click', function (ev) {
      // Belt and braces with keepalive below: a click that lands in the
      // millisecond before the response does should not navigate.
      if (open.getAttribute('aria-disabled') === 'true') ev.preventDefault();
    });
  }

  // Same-origin, so this is an ordinary request with no CORS involved.
  //
  // keepalive lets the request outlive this document, so a visitor who
  // navigates away mid-flight still has their measurement recorded. The payload
  // is a few hundred bytes, far inside the 64KB the browser allows.
  fetch('/v1/collect', {
    method: 'POST',
    body: JSON.stringify(payload),
    keepalive: true
  }).then(function (res) {
    if (!res.ok) throw new Error('server answered ' + res.status);
    status.textContent = 'Recorded ' + payload.u + '.';
    status.removeAttribute('data-state');
    settleLink('Open the dashboard');
    // The fragment is a one-shot payload. Clearing it means a reload does not
    // silently record the same measurement twice.
    history.replaceState(null, '', location.pathname);
  }).catch(function (err) {
    fail('Could not record the snapshot: ' + err.message);
  });
})();

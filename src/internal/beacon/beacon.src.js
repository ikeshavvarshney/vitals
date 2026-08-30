/*
 * vitals beacon: readable source.
 *
 * This file is the version a human reviews. `beacon.min.js` is the version that
 * ships, minified by hand because there is no minifier in this project to run.
 * The two are kept in sync by hand; `make beacon` enforces the size budget on
 * the minified file, and beacon_test.go asserts the two stay structurally
 * equivalent.
 *
 * What it collects, and how honest each number is:
 *
 *   TTFB  exact. responseStart from the navigation entry.
 *   FCP   exact. The first-contentful-paint entry.
 *   LCP   exact as of page hide. The last largest-contentful-paint entry seen.
 *   CLS   correct algorithm. Session windows: a shift joins the current window
 *         if it is within 1s of the previous shift and 5s of the window start,
 *         otherwise it starts a new one. The reported value is the largest
 *         window, which is what the specification defines.
 *   INP   APPROXIMATED. Real INP tracks full interaction latency and reports a
 *         high percentile of it. This reports the maximum event duration, which
 *         is directionally right and wrong in the tail. Documented as such.
 *
 * Not handled, and web-vitals handles all of these: back-forward cache
 * restoration, soft navigations, prerendering, and several older-Safari quirks.
 */
(function () {
  var ENDPOINT = '/v1/collect';
  var metrics = {};
  var sent = 0;

  // Every observer is wrapped: an entry type the browser does not support
  // throws on observe(), and one unsupported type must not stop the rest.
  // buffered:true replays entries that occurred before this script ran, which
  // is what makes a deferred script still see TTFB and FCP.
  // durationThreshold is only meaningful for 'event' entries. Passing it for
  // every type is harmless: an observer ignores dictionary members it does not
  // recognise, and folding it in avoids an options-merging loop.
  function observe(type, callback, durationThreshold) {
    try {
      new PerformanceObserver(callback).observe({
        type: type,
        buffered: true,
        durationThreshold: durationThreshold
      });
    } catch (e) {
      /* Unsupported entry type. The metric is simply not reported. */
    }
  }

  observe('navigation', function (list) {
    var entry = list.getEntries()[0];
    if (entry) metrics.ttfb = entry.responseStart;
  });

  observe('paint', function (list) {
    list.getEntries().forEach(function (entry) {
      if (entry.name === 'first-contentful-paint') metrics.fcp = entry.startTime;
    });
  });

  observe('largest-contentful-paint', function (list) {
    var entries = list.getEntries();
    // The last entry wins: LCP is revised upward until the page is hidden.
    metrics.lcp = entries[entries.length - 1].startTime;
  });

  // CLS session windows.
  var windowValue = 0;
  var windowStart = 0;
  var windowLast = 0;
  var worst = 0;

  observe('layout-shift', function (list) {
    list.getEntries().forEach(function (entry) {
      // A shift within 500ms of a user interaction is expected, not a defect.
      if (entry.hadRecentInput) return;

      if (
        windowValue &&
        entry.startTime - windowLast < 1000 &&
        entry.startTime - windowStart < 5000
      ) {
        windowValue += entry.value;
        windowLast = entry.startTime;
      } else {
        windowValue = entry.value;
        windowStart = windowLast = entry.startTime;
      }

      if (windowValue > worst) {
        worst = windowValue;
        metrics.cls = worst;
      }
    });
  });

  // INP approximation. durationThreshold:16 skips events faster than one frame,
  // which is the overwhelming majority of them.
  observe(
    'event',
    function (list) {
      list.getEntries().forEach(function (entry) {
        if (entry.duration > (metrics.inp || 0)) metrics.inp = entry.duration;
      });
    },
    16
  );

  // Flush once, when the page is hidden.
  //
  // Not on 'unload': that event is unreliable on mobile and prevents the page
  // from entering the back-forward cache. visibilitychange to hidden is the
  // last moment guaranteed to run.
  function flush() {
    if (sent || document.visibilityState !== 'hidden') return;
    sent = 1;

    var body = JSON.stringify({
      u: location.pathname,
      t: Date.now(),
      w: innerWidth,
      m: metrics
    });

    // sendBeacon survives the page being torn down. If it is unavailable or
    // refuses (its queue is full), fall back to keepalive fetch. The rejection
    // is swallowed: there is nothing useful to do about a dropped sample, and
    // an unhandled rejection would show up in the visitor's console.
    if (!navigator.sendBeacon?.(ENDPOINT, body)) {
      fetch(ENDPOINT, { method: 'POST', body: body, keepalive: true }).catch(
        function () {}
      );
    }
  }

  addEventListener('visibilitychange', flush);
})();

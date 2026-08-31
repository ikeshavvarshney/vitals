/*
 * vitals full beacon: readable source.
 *
 * This is the parity build. `beacon.src.js` is the 942-byte default that most
 * sites should use; this one costs about twice that and closes the accuracy
 * gaps that the small one documents as known limitations. Both are served from
 * this binary, both post to the same endpoint, and a site picks one by which
 * script tag it writes.
 *
 *   /b.js       small, fast, approximate INP, page loads only
 *   /b-full.js  this file: real INP, bfcache, soft navigations, prerender
 *               correction, element attribution
 *
 * What it collects, and how honest each number is:
 *
 *   TTFB  exact. responseStart, corrected for prerender activation.
 *   FCP   exact. The first-contentful-paint entry, activation-corrected, and
 *         discarded if the page was already hidden when it was reported.
 *   LCP   exact as of page hide, same corrections as FCP.
 *   CLS   correct algorithm. Session windows: a shift joins the current window
 *         if it is within 1s of the previous shift and 5s of the window start,
 *         otherwise it starts a new one. The reported value is the largest
 *         window, which is what the specification defines.
 *   INP   real, not approximated. Event entries are grouped by interactionId,
 *         each interaction's latency is the longest event in it, and the
 *         reported value is the interaction one place in from the top for every
 *         50 interactions on the page. That is the high percentile the INP
 *         specification defines, and it is what web-vitals reports.
 *
 * Where this file is still weaker than web-vitals, stated rather than hidden:
 *
 *   - Only the ten longest interactions are retained. web-vitals keeps the same
 *     ten, so the reported percentile agrees for any page with fewer than about
 *     500 interactions and can differ above that.
 *   - An interaction evicted from the top ten never re-enters it, even if a
 *     later event in that same interaction is slower. web-vitals has the same
 *     bound in practice; the window in which it matters is small and it can
 *     only under-report.
 *   - Soft navigations are detected by wrapping history.pushState and
 *     history.replaceState, not by the soft-navigation performance entry, which
 *     ships behind a flag. A route change performed without the History API is
 *     missed, and a soft navigation reports CLS and INP only: no browser
 *     re-fires LCP or FCP for one.
 *   - After a back-forward cache restore, FCP and LCP are reported as the time
 *     from the restore to the first frame, measured from the pageshow event.
 *     That is an approximation of what a real paint observer would say.
 *   - Attribution is one CSS-ish selector per metric, built from the tag, id,
 *     and first class. It is not a unique path, so two sibling elements can
 *     report the same selector. web-vitals reports the full subpart timing
 *     breakdown (input delay, processing, presentation) and long-animation-frame
 *     data; this reports only which element was responsible.
 *   - No workarounds for older Safari's paint-timing quirks.
 */
(function () {
  var ENDPOINT = '/v1/collect';
  var HIDDEN = 'hidden';

  // How many interactions are retained for the INP percentile, and how many
  // interactions on the page buy one more discarded from the top. Both are the
  // values the INP specification uses.
  var INP_RETAINED = 10;
  var INP_PER_DISCARD = 50;

  // Prerender correction. A prerendered page starts its clock when the browser
  // began rendering it in the background, which can be many seconds before the
  // visitor navigated to it. activationStart is the offset between the two, and
  // every paint timing has to be measured from it instead of from zero, or a
  // prerendered page reports an LCP that is worse than what anyone saw.
  //
  // Read synchronously rather than through the observer below: the observer
  // callback is asynchronous, and a paint entry can arrive before it runs.
  var nav = performance.getEntriesByType('navigation')[0];
  var activationStart = (nav && nav.activationStart) || 0;

  // The moment the page first became hidden, or Infinity if it never has.
  //
  // A page opened in a background tab still fires paint entries, but they
  // describe a frame nobody looked at: the LCP of a tab that was never in front
  // is meaningless, and averaging it into the distribution drags the whole site
  // down. Any paint reported after this instant is discarded.
  var firstHidden = document.visibilityState === HIDDEN ? 0 : Infinity;

  // Per-page-view state. Everything here is reset by a soft navigation or a
  // back-forward cache restore, because both start a new page view.
  var metrics, attribution, id, navType, sent, route;

  // CLS session window state.
  var winValue, winStart, winLast, winTopValue, winTopTarget, clsMax;

  // INP interaction state. byId maps an interactionId to its record; ranked
  // holds the same records, longest first.
  var byId, ranked, interactionCount, longestEvent, sawInteractionId;

  reset('');

  // reset starts a new page view. kind is the navigation type to report for it,
  // or '' to take the type from the navigation entry.
  function reset(kind) {
    metrics = {};
    attribution = {};
    id = uid();
    navType = kind || (activationStart > 0 ? 'prerender' : (nav && nav.type) || '');
    sent = 0;
    route = location.pathname;

    winValue = winStart = winLast = winTopValue = clsMax = 0;
    winTopTarget = '';

    byId = {};
    ranked = [];
    interactionCount = longestEvent = 0;
    sawInteractionId = false;
  }

  // uid is a per-page-view identifier, used by the server to drop a payload it
  // has already stored. sendBeacon and the keepalive fetch fallback can both
  // land in a rare race, and a soft navigation sends several payloads from one
  // page. Math.random is not cryptographically strong and does not need to be:
  // this is a deduplication key, never an authentication one.
  function uid() {
    return Math.random().toString(36).slice(2, 10) + Date.now().toString(36);
  }

  // corrected subtracts the prerender activation offset from a paint timing,
  // floored at zero for a browser that reports the two inconsistently.
  function corrected(value) {
    return value > activationStart ? value - activationStart : 0;
  }

  // selector names an element the way a developer would recognise it: the tag,
  // then the id if it has one, otherwise the first class. It is deliberately
  // not a unique path. A full path is long, brittle against any DOM change, and
  // useless in an aggregate: "div#promo" grouped across a thousand page views
  // is the number worth having.
  function selector(node) {
    if (!node || node.nodeType !== 1) return '';

    var name = node.tagName.toLowerCase();
    if (node.id) return name + '#' + node.id;

    // SVG elements carry an SVGAnimatedString here rather than a string.
    var cls = typeof node.className === 'string' ? node.className.trim() : '';
    return cls ? name + '.' + cls.split(/\s+/)[0] : name;
  }

  // Every observer is wrapped: an entry type the browser does not support
  // throws on observe(), and one unsupported type must not stop the rest.
  // buffered:true replays entries that occurred before this script ran, which
  // is what makes a deferred script still see TTFB and FCP.
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
    if (entry) metrics.ttfb = corrected(entry.responseStart);
  });

  observe('paint', function (list) {
    list.getEntries().forEach(function (entry) {
      if (entry.name === 'first-contentful-paint' && entry.startTime < firstHidden) {
        metrics.fcp = corrected(entry.startTime);
      }
    });
  });

  observe('largest-contentful-paint', function (list) {
    var entries = list.getEntries();
    // The last entry wins: LCP is revised upward until the page is hidden.
    var entry = entries[entries.length - 1];
    if (entry.startTime >= firstHidden) return;

    metrics.lcp = corrected(entry.startTime);
    // entry.url is set for an image candidate and empty for a text one, so the
    // element selector is the field that is always populated.
    attribution.lcp = selector(entry.element) || entry.url || '';
  });

  observe('layout-shift', function (list) {
    list.getEntries().forEach(function (entry) {
      // A shift within 500ms of a user interaction is expected, not a defect.
      if (entry.hadRecentInput) return;

      if (
        winValue &&
        entry.startTime - winLast < 1000 &&
        entry.startTime - winStart < 5000
      ) {
        winValue += entry.value;
        winLast = entry.startTime;
      } else {
        winValue = entry.value;
        winStart = winLast = entry.startTime;
        winTopValue = 0;
      }

      // The window is what CLS scores, but a developer needs one element to go
      // and look at, so the largest single shift in the window is remembered.
      if (entry.value > winTopValue) {
        winTopValue = entry.value;
        var sources = entry.sources || [];
        for (var i = 0; i < sources.length; i++) {
          var name = selector(sources[i].node);
          if (name) {
            winTopTarget = name;
            break;
          }
        }
      }

      if (winValue > clsMax) {
        clsMax = winValue;
        metrics.cls = clsMax;
        attribution.cls = winTopTarget;
      }
    });
  });

  // Real INP.
  //
  // One interaction produces several event entries: the pointerdown, the
  // pointerup, the click. They share an interactionId, and the interaction's
  // latency is the longest of them, not their sum. INP is then a high
  // percentile of those latencies rather than the maximum, so that one
  // pathological interaction on a long-lived page does not define the score:
  // the specification discards the worst interaction for every 50 on the page.
  function recordInteraction(entry) {
    var key = entry.interactionId;
    if (!key) return false;

    sawInteractionId = true;
    var found = byId[key];

    if (found) {
      if (entry.duration > found.d) {
        found.d = entry.duration;
        found.t = selector(entry.target) || found.t;
      }
      return true;
    }

    found = byId[key] = { d: entry.duration, t: selector(entry.target) };
    interactionCount++;
    ranked.push(found);
    return true;
  }

  function scoreInteractions() {
    ranked.sort(function (a, b) {
      return b.d - a.d;
    });
    ranked.length = Math.min(ranked.length, INP_RETAINED);

    var index = Math.min(
      ranked.length - 1,
      Math.floor(interactionCount / INP_PER_DISCARD)
    );
    var pick = ranked[index];
    if (!pick) return;

    metrics.inp = pick.d;
    attribution.inp = pick.t;
  }

  observe(
    'event',
    function (list) {
      var grouped = false;
      list.getEntries().forEach(function (entry) {
        if (recordInteraction(entry)) {
          grouped = true;
        } else if (entry.duration > longestEvent) {
          longestEvent = entry.duration;
        }
      });

      if (grouped) {
        scoreInteractions();
      } else if (!sawInteractionId) {
        // A browser that reports event timing without interactionId cannot be
        // grouped into interactions at all. Rather than report nothing, fall
        // back to the approximation the small beacon uses, which over-reports
        // in the tail. Once a single interactionId is seen this is abandoned.
        metrics.inp = longestEvent;
      }
    },
    // 16ms rather than the specification's default of 40: an interaction that
    // misses one frame is already worth seeing, and the cost of the extra
    // entries is a few objects on a page that is already interactive.
    16
  );

  // Flush once per page view.
  //
  // Not on 'unload': that event is unreliable on mobile and prevents the page
  // from entering the back-forward cache. visibilitychange to hidden is the
  // last moment guaranteed to run, and pagehide covers the browsers that tear a
  // page down without a visibility change.
  function send() {
    if (sent) return;
    sent = 1;

    var body = JSON.stringify({
      u: route,
      t: Date.now(),
      w: innerWidth,
      i: id,
      n: navType,
      m: metrics,
      a: attribution
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

  function onHidden() {
    if (document.visibilityState !== HIDDEN) return;
    if (firstHidden === Infinity) firstHidden = performance.now();
    send();
  }

  addEventListener('visibilitychange', onHidden);
  addEventListener('pagehide', send);

  // Back-forward cache restore. The page was not reloaded, so no paint entry
  // will fire again, but the visitor is looking at it as a new page view and
  // every layout shift and interaction from here belongs to that view.
  //
  // FCP and LCP are reported as the delay between the restore and the first
  // frame after it. A restore is usually near-instant, so these are small
  // numbers that reflect the restore rather than a real paint, and they are
  // labelled with a navigation type that says so.
  addEventListener('pageshow', function (event) {
    if (!event.persisted) return;

    reset('back-forward-cache');
    requestAnimationFrame(function () {
      var restored = performance.now() - event.timeStamp;
      metrics.ttfb = 0;
      metrics.fcp = metrics.lcp = restored > 0 ? restored : 0;
    });
  });

  // Soft navigations.
  //
  // A single-page app changes route without a page load, so nothing above
  // fires: the same page view would otherwise accumulate the layout shifts and
  // interactions of every route the visitor passed through and file them all
  // under the first one. Wrapping the History API is how a client-side router
  // announces itself, since the soft-navigation performance entry is still
  // behind a flag in every browser.
  //
  // The wrapper calls through first and reports second, so a router that throws
  // fails exactly as it would have without this script attached.
  function onRouteChange() {
    if (location.pathname === route) return; // a state change, not a navigation

    if (Object.keys(metrics).length) send();
    reset('soft-navigation');
  }

  ['pushState', 'replaceState'].forEach(function (method) {
    var original = history[method];
    if (typeof original !== 'function') return;

    history[method] = function () {
      var result = original.apply(this, arguments);
      onRouteChange();
      return result;
    };
  });

  addEventListener('popstate', onRouteChange);
})();

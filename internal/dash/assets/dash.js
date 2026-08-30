/*
 * vitals dashboard.
 *
 * Vanilla JavaScript, no framework, no bundler, no build step. Charts are
 * inline SVG built from the API response: a polyline for the series, rects and
 * text for the axes. This is roughly what a charting library would draw, minus
 * the library.
 */
(function () {
  'use strict';

  var el = {
    status: document.getElementById('status'),
    scorecard: document.getElementById('scorecard'),
    series: document.getElementById('series'),
    seriesSub: document.getElementById('series-sub'),
    seriesCaption: document.getElementById('series-caption'),
    routes: document.getElementById('routes'),
    devices: document.getElementById('devices'),
    counters: document.getElementById('counters'),
    windowSel: document.getElementById('window'),
    metricSel: document.getElementById('metric'),
    refresh: document.getElementById('refresh')
  };

  var METRIC_LABELS = {
    lcp: 'LCP', inp: 'INP', cls: 'CLS', fcp: 'FCP', ttfb: 'TTFB'
  };
  var METRIC_TITLES = {
    lcp: 'Largest Contentful Paint',
    inp: 'Interaction to Next Paint (approximated)',
    cls: 'Cumulative Layout Shift',
    fcp: 'First Contentful Paint',
    ttfb: 'Time to First Byte'
  };

  // ---------------------------------------------------------------- helpers

  function h(tag, attrs, children) {
    var node = document.createElement(tag);
    for (var k in attrs) {
      if (k === 'class') node.className = attrs[k];
      else if (k === 'text') node.textContent = attrs[k];
      else node.setAttribute(k, attrs[k]);
    }
    (children || []).forEach(function (c) { node.appendChild(c); });
    return node;
  }

  function svg(tag, attrs) {
    var node = document.createElementNS('http://www.w3.org/2000/svg', tag);
    for (var k in attrs) node.setAttribute(k, attrs[k]);
    return node;
  }

  // formatValue keeps the number readable without implying precision the
  // bucketed percentile does not have.
  function formatValue(v, unit) {
    if (v === null || v === undefined) return null;
    if (unit === '') return v.toFixed(3);          // CLS
    if (v >= 10000) return (v / 1000).toFixed(2) + ' s';
    if (v >= 1000) return Math.round(v).toLocaleString();
    if (v >= 100) return Math.round(v).toString();
    return v.toFixed(1);
  }

  function formatUnit(v, unit) {
    if (unit === '' || v === null) return '';
    return v >= 10000 ? '' : 'ms';
  }

  function formatBytes(n) {
    if (n < 1024) return n + ' B';
    return (n / 1024).toFixed(1) + ' KB';
  }

  function formatClock(iso) {
    var d = new Date(iso);
    return String(d.getHours()).padStart(2, '0') + ':' +
      String(d.getMinutes()).padStart(2, '0');
  }

  function setStatus(text, state) {
    el.status.textContent = text;
    if (state) el.status.setAttribute('data-state', state);
    else el.status.removeAttribute('data-state');
  }

  function clear(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
  }

  function getJSON(url) {
    return fetch(url, { headers: { accept: 'application/json' } }).then(function (r) {
      return r.json().then(function (body) {
        if (!r.ok) throw new Error(body.error || ('HTTP ' + r.status));
        return body;
      });
    });
  }

  // ------------------------------------------------------------- scorecard

  function renderScorecard(data) {
    clear(el.scorecard);

    data.metrics.forEach(function (m) {
      var value = formatValue(m.p75, m.unit);
      var band = m.band || '';

      var name = h('div', { class: 'card-name' }, [
        h('span', { class: 'pip' }),
        h('span', { text: METRIC_LABELS[m.metric] || m.metric })
      ]);

      var valueNode;
      if (value === null) {
        valueNode = h('div', { class: 'card-value card-none', text: 'no data' });
      } else {
        valueNode = h('div', { class: 'card-value' }, [
          h('span', { text: value }),
          h('span', { class: 'unit', text: formatUnit(m.p75, m.unit) })
        ]);
      }

      var meta = m.samples === 1 ? '1 sample' : m.samples.toLocaleString() + ' samples';

      var card = h('div', { class: 'card ' + band, title: METRIC_TITLES[m.metric] || '' }, [
        name, valueNode, h('div', { class: 'card-meta', text: meta })
      ]);
      el.scorecard.appendChild(card);
    });
  }

  // ----------------------------------------------------------------- chart

  var CHART = { w: 900, h: 260, top: 16, right: 16, bottom: 28, left: 52 };

  function renderSeries(data) {
    clear(el.series);

    var points = data.buckets.filter(function (b) { return b.p75 !== null; });
    if (points.length === 0) {
      el.series.appendChild(h('p', {
        class: 'empty',
        text: 'No samples in this window. Open the demo site and click through a few pages.'
      }));
      el.seriesCaption.textContent = '';
      return;
    }

    var innerW = CHART.w - CHART.left - CHART.right;
    var innerH = CHART.h - CHART.top - CHART.bottom;

    // The y axis always includes the good threshold, so a healthy chart still
    // shows where the limit is rather than zooming into noise.
    var maxValue = Math.max(
      data.good * 1.15,
      points.reduce(function (a, b) { return Math.max(a, b.p75); }, 0) * 1.1
    );

    var n = data.buckets.length;
    function x(i) { return CHART.left + (n === 1 ? innerW / 2 : (i / (n - 1)) * innerW); }
    function y(v) { return CHART.top + innerH - (v / maxValue) * innerH; }

    var root = svg('svg', {
      viewBox: '0 0 ' + CHART.w + ' ' + CHART.h,
      role: 'img',
      'aria-label': (METRIC_TITLES[data.metric] || data.metric) +
        ' 75th percentile over time, ' + points.length + ' buckets with data'
    });

    // Horizontal grid and y labels.
    for (var g = 0; g <= 4; g++) {
      var value = (maxValue / 4) * g;
      var gy = y(value);
      root.appendChild(svg('line', {
        class: 'grid', x1: CHART.left, x2: CHART.w - CHART.right, y1: gy, y2: gy
      }));
      var label = svg('text', { class: 'tick', x: CHART.left - 8, y: gy + 3, 'text-anchor': 'end' });
      label.textContent = formatValue(value, data.unit);
      root.appendChild(label);
    }

    // Threshold lines, drawn only when they fall inside the visible range.
    [
      { v: data.good, cls: 'thresh-good', text: 'good' },
      { v: data.needsImprovement, cls: 'thresh-needs', text: 'needs improvement' }
    ].forEach(function (t) {
      if (t.v > maxValue) return;
      var ty = y(t.v);
      root.appendChild(svg('line', {
        class: 'thresh ' + t.cls, x1: CHART.left, x2: CHART.w - CHART.right, y1: ty, y2: ty
      }));
      var tl = svg('text', {
        class: 'thresh-label ' + t.cls, x: CHART.w - CHART.right, y: ty - 4, 'text-anchor': 'end'
      });
      tl.setAttribute('fill', 'currentColor');
      tl.textContent = t.text;
      root.appendChild(tl);
    });

    // Axes.
    root.appendChild(svg('line', {
      class: 'axis', x1: CHART.left, x2: CHART.left, y1: CHART.top, y2: CHART.top + innerH
    }));
    root.appendChild(svg('line', {
      class: 'axis', x1: CHART.left, x2: CHART.w - CHART.right,
      y1: CHART.top + innerH, y2: CHART.top + innerH
    }));

    // The line itself. Gaps are real gaps: a bucket with no samples breaks the
    // polyline rather than interpolating a value nobody measured.
    var runs = [];
    var run = [];
    data.buckets.forEach(function (b, i) {
      if (b.p75 === null) {
        if (run.length) { runs.push(run); run = []; }
        return;
      }
      run.push({ x: x(i), y: y(b.p75), b: b });
    });
    if (run.length) runs.push(run);

    runs.forEach(function (r) {
      if (r.length > 1) {
        root.appendChild(svg('polyline', {
          class: 'line',
          points: r.map(function (p) { return p.x + ',' + p.y; }).join(' ')
        }));
      }
      r.forEach(function (p) {
        var c = svg('circle', { class: 'point ' + (p.b.band || ''), cx: p.x, cy: p.y, r: r.length > 60 ? 1.5 : 2.5 });
        var title = svg('title');
        title.textContent = formatClock(p.b.t) + '  ' +
          formatValue(p.b.p75, data.unit) + (data.unit ? ' ms' : '') +
          '  (' + p.b.samples + ' samples)';
        c.appendChild(title);
        root.appendChild(c);
      });
    });

    // X labels: first, middle, last. More would crowd at mobile widths.
    [0, Math.floor((n - 1) / 2), n - 1].forEach(function (i, k) {
      var t = svg('text', {
        class: 'tick', x: x(i), y: CHART.h - 8,
        'text-anchor': k === 0 ? 'start' : (k === 2 ? 'end' : 'middle')
      });
      t.textContent = formatClock(data.buckets[i].t);
      root.appendChild(t);
    });

    el.series.appendChild(root);

    var mins = Math.round(data.bucketSeconds / 60);
    el.seriesCaption.textContent = points.length + ' of ' + n +
      ' buckets have samples, ' + (mins >= 1 ? mins + ' min' : Math.round(data.bucketSeconds) + ' s') +
      ' per bucket. Gaps are windows with no page views.';
  }

  // ---------------------------------------------------------------- tables

  function renderTable(table, data, keyHeading) {
    clear(table);

    if (!data.rows.length) {
      var wrap = table.parentNode;
      clear(table);
      table.appendChild(h('caption', { class: 'visually-hidden', text: keyHeading }));
      var tb = h('tbody', {}, [h('tr', {}, [
        h('td', { class: 'empty', colspan: '4', text: 'No samples in this window.' })
      ])]);
      table.appendChild(tb);
      void wrap;
      return;
    }

    table.appendChild(h('thead', {}, [h('tr', {}, [
      h('th', { text: keyHeading }),
      h('th', { text: 'p75' }),
      h('th', { text: 'Band' }),
      h('th', { text: 'Samples' })
    ])]));

    var body = h('tbody', {});
    data.rows.forEach(function (row) {
      var value = formatValue(row.p75, data.unit);
      body.appendChild(h('tr', {}, [
        h('td', { class: 'key', text: row.key || '(unknown)' }),
        h('td', { class: 'num', text: value === null ? '—' : value + (data.unit ? ' ms' : '') }),
        h('td', {}, [h('span', { class: 'badge ' + (row.band || ''), text: row.band || 'no data' })]),
        h('td', { class: 'num', text: row.samples.toLocaleString() })
      ]));
    });
    table.appendChild(body);
  }

  // -------------------------------------------------------------- counters

  function renderCounters(data) {
    clear(el.counters);

    var items = [
      ['accepted', data.ingest.accepted],
      ['malformed', data.ingest.malformed],
      ['too large', data.ingest.tooLarge],
      ['store errors', data.ingest.storeErrors],
      ['records held', data.coverage ? data.coverage.total : 0]
    ];

    items.forEach(function (pair) {
      el.counters.appendChild(h('div', {}, [
        h('dt', { text: pair[0] }),
        h('dd', { text: (pair[1] || 0).toLocaleString() })
      ]));
    });
  }

  // ---------------------------------------------------------- weight ledger

  // The tool reports its own weight. This is measured, not asserted: it reads
  // the resource timings for this very page.
  function renderLedger() {
    if (!window.performance || !performance.getEntriesByType) return;

    var entries = performance.getEntriesByType('resource');
    var nav = performance.getEntriesByType('navigation')[0];

    var total = nav && nav.transferSize ? nav.transferSize : 0;
    var beacon = 0;

    entries.forEach(function (e) {
      var size = e.transferSize || 0;
      total += size;
      if (e.name.indexOf('/b.js') !== -1) beacon = size;
    });

    // transferSize is 0 for a cached response, which would otherwise read as
    // "this page weighs nothing".
    var pageText = total > 0 ? formatBytes(total) : 'cached';
    document.getElementById('ledger-page').textContent = pageText;
    document.getElementById('ledger-requests').textContent = String(entries.length + 1);

    // The beacon is not loaded by the dashboard itself, so fetch its size from
    // the header the beacon handler advertises.
    if (beacon > 0) {
      document.getElementById('ledger-beacon').textContent = formatBytes(beacon);
    } else {
      fetch('/b.js', { method: 'HEAD' }).then(function (r) {
        var bytes = r.headers.get('x-beacon-bytes');
        document.getElementById('ledger-beacon').textContent =
          bytes ? formatBytes(parseInt(bytes, 10)) : '—';
      }).catch(function () {});
    }
  }

  // ------------------------------------------------------------------ load

  function load() {
    var win = el.windowSel.value;
    var metric = el.metricSel.value;
    var q = 'from=' + encodeURIComponent(win);

    setStatus('loading…');

    Promise.all([
      getJSON('/api/summary?' + q),
      getJSON('/api/series?' + q + '&metric=' + metric + '&n=48'),
      getJSON('/api/routes?' + q + '&metric=' + metric),
      getJSON('/api/devices?' + q + '&metric=' + metric)
    ]).then(function (r) {
      renderScorecard(r[0]);
      renderCounters(r[0]);
      renderSeries(r[1]);
      renderTable(el.routes, r[2], 'Route');
      renderTable(el.devices, r[3], 'Device');

      el.seriesSub.textContent = METRIC_TITLES[metric] || '';
      setStatus(r[0].samples.toLocaleString() + ' page views in window');
    }).catch(function (err) {
      setStatus(err.message || 'request failed', 'error');
    });
  }

  el.refresh.addEventListener('click', load);
  el.windowSel.addEventListener('change', load);
  el.metricSel.addEventListener('change', load);

  renderLedger();
  load();
})();

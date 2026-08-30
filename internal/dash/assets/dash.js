/*
 * vitals dashboard.
 *
 * Vanilla JavaScript, no framework, no bundler, no build step. Charts are inline
 * SVG built from the API response: a polygon for the area, a polyline for the
 * series, and lines and text for axes, gridlines, and thresholds.
 */
(function () {
  'use strict';

  var el = {
    status: document.getElementById('status'),
    scorecard: document.getElementById('scorecard'),
    series: document.getElementById('series'),
    seriesSub: document.getElementById('series-sub'),
    seriesCaption: document.getElementById('series-caption'),
    seriesReadout: document.getElementById('series-readout'),
    routes: document.getElementById('routes'),
    devices: document.getElementById('devices'),
    counters: document.getElementById('counters'),
    windowSel: document.getElementById('window'),
    metricGroup: document.getElementById('metric-group'),
    refresh: document.getElementById('refresh')
  };

  var METRICS = [
    { key: 'lcp', label: 'LCP', name: 'Largest Contentful Paint' },
    { key: 'inp', label: 'INP', name: 'Interaction to Next Paint, approximated' },
    { key: 'cls', label: 'CLS', name: 'Cumulative Layout Shift' },
    { key: 'fcp', label: 'FCP', name: 'First Contentful Paint' },
    { key: 'ttfb', label: 'TTFB', name: 'Time to First Byte' }
  ];

  var metricByKey = {};
  METRICS.forEach(function (m) { metricByKey[m.key] = m; });

  var selectedMetric = 'lcp';

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

  function clear(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
  }

  // formatValue keeps the number readable without implying precision the
  // bucketed percentile does not have.
  function formatValue(v, unit) {
    if (v === null || v === undefined) return null;
    if (unit === '') return v.toFixed(3);
    if (v >= 10000) return (v / 1000).toFixed(2);
    if (v >= 1000) return Math.round(v).toLocaleString();
    if (v >= 100) return Math.round(v).toString();
    return v.toFixed(1);
  }

  function unitLabel(v, unit) {
    if (unit === '' || v === null) return '';
    return v >= 10000 ? 's' : 'ms';
  }

  function formatBytes(n) {
    if (!n) return '0 B';
    return n < 1024 ? n + ' B' : (n / 1024).toFixed(1) + ' KB';
  }

  function pad2(n) { return String(n).padStart(2, '0'); }

  function formatClock(iso) {
    var d = new Date(iso);
    return pad2(d.getHours()) + ':' + pad2(d.getMinutes());
  }

  function formatDateTime(iso) {
    var d = new Date(iso);
    return pad2(d.getDate()) + '/' + pad2(d.getMonth() + 1) + ' ' +
      pad2(d.getHours()) + ':' + pad2(d.getMinutes());
  }

  function setStatus(text, state) {
    el.status.textContent = text;
    if (state) el.status.setAttribute('data-state', state);
    else el.status.removeAttribute('data-state');
  }

  function getJSON(url) {
    return fetch(url, { headers: { accept: 'application/json' } }).then(function (r) {
      return r.json().then(function (body) {
        if (!r.ok) throw new Error(body.error || ('HTTP ' + r.status));
        return body;
      });
    });
  }

  // ------------------------------------------------------- metric selector

  function buildMetricSelector() {
    clear(el.metricGroup);

    METRICS.forEach(function (m) {
      var b = h('button', {
        type: 'button',
        role: 'radio',
        'aria-checked': m.key === selectedMetric ? 'true' : 'false',
        'data-metric': m.key,
        title: m.name,
        text: m.label
      });
      b.addEventListener('click', function () {
        if (selectedMetric === m.key) return;
        selectedMetric = m.key;
        buildMetricSelector();
        load();
      });
      el.metricGroup.appendChild(b);
    });
  }

  // ------------------------------------------------------------- scorecard

  // trackFor renders where a value sits across the three bands. The scale runs
  // to 1.5x the needs-improvement threshold, so a poor value has somewhere to
  // sit rather than pinning to the end.
  function trackFor(m) {
    var scaleMax = m.needsImprovement * 1.5;
    var goodPct = (m.good / scaleMax) * 100;
    var needsPct = ((m.needsImprovement - m.good) / scaleMax) * 100;

    var bar = h('div', { class: 'track-bar' }, [
      h('div', { class: 'track-seg good', style: 'width:' + goodPct + '%' }),
      h('div', { class: 'track-seg needs', style: 'width:' + needsPct + '%' }),
      h('div', { class: 'track-seg poor', style: 'width:' + (100 - goodPct - needsPct) + '%' })
    ]);

    if (m.p75 !== null) {
      var pos = Math.min(100, Math.max(0, (m.p75 / scaleMax) * 100));
      bar.appendChild(h('div', {
        class: 'track-marker',
        style: 'left:' + pos + '%',
        title: formatValue(m.p75, m.unit) + ' against ' +
          formatValue(m.good, m.unit) + ' and ' + formatValue(m.needsImprovement, m.unit)
      }));
    }
    return bar;
  }

  function renderScorecard(data) {
    clear(el.scorecard);

    data.metrics.forEach(function (m) {
      var meta = metricByKey[m.metric] || { label: m.metric, name: '' };
      var value = formatValue(m.p75, m.unit);

      var valueNode = value === null
        ? h('div', { class: 'card-value empty', text: 'No data' })
        : h('div', { class: 'card-value' }, [
            h('span', { text: value }),
            h('span', { class: 'unit', text: unitLabel(m.p75, m.unit) })
          ]);

      var samples = m.samples === 1
        ? '1 sample'
        : m.samples.toLocaleString() + ' samples';

      el.scorecard.appendChild(h('div', {
        class: 'card',
        'data-metric': m.metric,
        title: meta.name
      }, [
        h('div', { class: 'card-head' }, [
          h('span', { class: 'card-abbr', text: meta.label }),
          h('span', { class: 'card-full', text: m.unit === '' ? 'score' : 'ms' })
        ]),
        valueNode,
        trackFor(m),
        h('div', { class: 'card-foot' }, [
          h('span', {
            class: 'badge ' + (m.band || 'none'),
            text: m.band ? m.band.replace('-', ' ') : 'no data'
          }),
          h('span', { class: 'card-samples', text: samples })
        ])
      ]));
    });
  }

  // ----------------------------------------------------------------- chart

  var CHART = { w: 940, h: 250, top: 14, right: 14, bottom: 26, left: 54 };

  function renderSeries(data) {
    clear(el.series);
    el.seriesReadout.classList.remove('on');

    var points = data.buckets.filter(function (b) { return b.p75 !== null; });
    if (points.length === 0) {
      el.series.appendChild(h('p', {
        class: 'empty',
        text: 'No samples in this window. Open the demo site, click through a few pages, then switch tabs so the beacon reports.'
      }));
      el.seriesCaption.textContent = '';
      return;
    }

    var innerW = CHART.w - CHART.left - CHART.right;
    var innerH = CHART.h - CHART.top - CHART.bottom;
    var baseline = CHART.top + innerH;

    // The scale always covers the good threshold, so a healthy chart still
    // shows where the limit is rather than zooming into noise.
    var peak = points.reduce(function (a, b) { return Math.max(a, b.p75); }, 0);
    var maxValue = Math.max(data.good * 1.25, peak * 1.15);

    var n = data.buckets.length;
    function x(i) { return CHART.left + (n === 1 ? innerW / 2 : (i / (n - 1)) * innerW); }
    function y(v) { return CHART.top + innerH - Math.min(v / maxValue, 1) * innerH; }

    var root = svg('svg', {
      viewBox: '0 0 ' + CHART.w + ' ' + CHART.h,
      preserveAspectRatio: 'none',
      style: '--series-hue: var(--' + data.metric + ')',
      role: 'img',
      'aria-label': (metricByKey[data.metric] || {}).name +
        ', 75th percentile over time. ' + points.length + ' of ' + n +
        ' intervals contain samples.'
    });

    // Shaded threshold bands, so the good region is readable at a glance.
    var goodTop = y(Math.min(data.good, maxValue));
    var needsTop = y(Math.min(data.needsImprovement, maxValue));
    root.appendChild(svg('rect', {
      class: 'band-good', x: CHART.left, y: goodTop,
      width: innerW, height: Math.max(0, baseline - goodTop)
    }));
    root.appendChild(svg('rect', {
      class: 'band-needs', x: CHART.left, y: needsTop,
      width: innerW, height: Math.max(0, goodTop - needsTop)
    }));
    root.appendChild(svg('rect', {
      class: 'band-poor', x: CHART.left, y: CHART.top,
      width: innerW, height: Math.max(0, needsTop - CHART.top)
    }));

    // Gridlines and y labels.
    for (var g = 0; g <= 4; g++) {
      var gv = (maxValue / 4) * g;
      var gy = y(gv);
      root.appendChild(svg('line', {
        class: 'grid', x1: CHART.left, x2: CHART.w - CHART.right, y1: gy, y2: gy
      }));
      var lab = svg('text', {
        class: 'tick', x: CHART.left - 8, y: gy + 3, 'text-anchor': 'end'
      });
      lab.textContent = formatValue(gv, data.unit);
      root.appendChild(lab);
    }

    // Threshold lines, only where they fall inside the visible range.
    [{ v: data.good, cls: 'thresh-good' }, { v: data.needsImprovement, cls: 'thresh-needs' }]
      .forEach(function (t) {
        if (t.v > maxValue) return;
        var ty = y(t.v);
        root.appendChild(svg('line', {
          class: 'thresh ' + t.cls, x1: CHART.left, x2: CHART.w - CHART.right, y1: ty, y2: ty
        }));
      });

    root.appendChild(svg('line', {
      class: 'axis', x1: CHART.left, x2: CHART.left, y1: CHART.top, y2: baseline
    }));
    root.appendChild(svg('line', {
      class: 'axis', x1: CHART.left, x2: CHART.w - CHART.right, y1: baseline, y2: baseline
    }));

    // Gaps are real. A bucket with no samples breaks the line rather than
    // interpolating a value nobody measured.
    var runs = [], run = [];
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
        root.appendChild(svg('polygon', {
          class: 'area',
          points: r[0].x + ',' + baseline + ' ' +
            r.map(function (p) { return p.x + ',' + p.y; }).join(' ') + ' ' +
            r[r.length - 1].x + ',' + baseline
        }));
        root.appendChild(svg('polyline', {
          class: 'line',
          points: r.map(function (p) { return p.x + ',' + p.y; }).join(' ')
        }));
      }
      r.forEach(function (p) {
        root.appendChild(svg('circle', {
          class: 'point', cx: p.x, cy: p.y, r: r.length > 60 ? 1.6 : 2.6
        }));
      });
    });

    // X labels: start, middle, end. More would crowd at mobile widths.
    [0, Math.floor((n - 1) / 2), n - 1].forEach(function (i, k) {
      var t = svg('text', {
        class: 'tick', x: x(i), y: CHART.h - 8,
        'text-anchor': k === 0 ? 'start' : (k === 2 ? 'end' : 'middle')
      });
      t.textContent = formatClock(data.buckets[i].t);
      root.appendChild(t);
    });

    var cursor = svg('line', {
      class: 'cursor', x1: 0, x2: 0, y1: CHART.top, y2: baseline, opacity: 0
    });
    root.appendChild(cursor);

    attachReadout(root, cursor, data, x);
    el.series.appendChild(root);

    var mins = Math.round(data.bucketSeconds / 60);
    var per = mins >= 60
      ? (mins / 60).toFixed(mins % 60 === 0 ? 0 : 1) + ' h'
      : (mins >= 1 ? mins + ' min' : Math.round(data.bucketSeconds) + ' s');
    el.seriesCaption.textContent =
      points.length + ' of ' + n + ' intervals contain samples, ' + per +
      ' per interval. Gaps are windows with no page views.';
  }

  // attachReadout wires pointer tracking so hovering the chart reports the
  // exact bucket under the cursor rather than an eyeballed value.
  function attachReadout(root, cursor, data, x) {
    function nearest(clientX) {
      var box = root.getBoundingClientRect();
      var vx = ((clientX - box.left) / box.width) * CHART.w;
      var best = -1, bestDist = Infinity;
      data.buckets.forEach(function (b, i) {
        if (b.p75 === null) return;
        var d = Math.abs(x(i) - vx);
        if (d < bestDist) { bestDist = d; best = i; }
      });
      return best;
    }

    root.addEventListener('pointermove', function (ev) {
      var i = nearest(ev.clientX);
      if (i < 0) return;
      var b = data.buckets[i];
      cursor.setAttribute('x1', x(i));
      cursor.setAttribute('x2', x(i));
      cursor.setAttribute('opacity', 1);
      el.seriesReadout.textContent =
        formatDateTime(b.t) + '   ' + formatValue(b.p75, data.unit) +
        unitLabel(b.p75, data.unit) + '   n=' + b.samples;
      el.seriesReadout.classList.add('on');
    });

    root.addEventListener('pointerleave', function () {
      cursor.setAttribute('opacity', 0);
      el.seriesReadout.classList.remove('on');
    });
  }

  // ---------------------------------------------------------------- tables

  function renderTable(table, data, keyHeading, emptyText) {
    clear(table);
    table.appendChild(h('caption', { class: 'visually-hidden', text: keyHeading }));

    if (!data.rows.length) {
      table.appendChild(h('tbody', {}, [
        h('tr', {}, [h('td', { class: 'empty', colspan: '4', text: emptyText })])
      ]));
      return;
    }

    table.appendChild(h('thead', {}, [h('tr', {}, [
      h('th', { text: keyHeading }),
      h('th', { text: 'p75' }),
      h('th', { text: 'Relative' }),
      h('th', { text: 'Samples' })
    ])]));

    var worst = data.rows.reduce(function (a, r) {
      return Math.max(a, r.p75 === null ? 0 : r.p75);
    }, 0) || 1;

    var body = h('tbody', {});
    data.rows.forEach(function (row) {
      var value = formatValue(row.p75, data.unit);
      var pct = row.p75 === null ? 0 : Math.max(2, (row.p75 / worst) * 100);

      body.appendChild(h('tr', {}, [
        h('td', { class: 'key', text: row.key || '(unknown)' }),
        h('td', {
          class: 'num',
          text: value === null ? '—' : value + unitLabel(row.p75, data.unit)
        }),
        h('td', { class: 'meter' }, [
          h('div', {
            class: 'meter-bar',
            title: row.band ? row.band.replace('-', ' ') : 'no data'
          }, [
            h('div', {
              class: 'meter-fill ' + (row.band || ''),
              style: 'width:' + pct + '%'
            })
          ])
        ]),
        h('td', { class: 'num', text: row.samples.toLocaleString() })
      ]));
    });
    table.appendChild(body);
  }

  // -------------------------------------------------------------- counters

  function renderCounters(data) {
    clear(el.counters);

    var items = [
      ['Accepted', data.ingest.accepted, false],
      ['Malformed', data.ingest.malformed, data.ingest.malformed > 0],
      ['Too large', data.ingest.tooLarge, data.ingest.tooLarge > 0],
      ['Store errors', data.ingest.storeErrors, data.ingest.storeErrors > 0],
      ['Records held', data.coverage ? data.coverage.total : 0, false]
    ];

    items.forEach(function (item) {
      el.counters.appendChild(h('div', { 'data-warn': item[2] ? 'true' : 'false' }, [
        h('dt', { text: item[0] }),
        h('dd', { text: (item[1] || 0).toLocaleString() })
      ]));
    });
  }

  // ---------------------------------------------------------- weight ledger

  // The tool reports its own weight, measured from this page's resource
  // timings rather than asserted.
  function renderLedger() {
    if (!window.performance || !performance.getEntriesByType) return;

    var entries = performance.getEntriesByType('resource');
    var nav = performance.getEntriesByType('navigation')[0];
    var total = nav && nav.transferSize ? nav.transferSize : 0;

    entries.forEach(function (e) { total += e.transferSize || 0; });

    document.getElementById('ledger-page').textContent =
      total > 0 ? formatBytes(total) : 'cached';
    document.getElementById('ledger-requests').textContent = String(entries.length + 1);
  }

  function renderBeaconSize(bytes) {
    if (!bytes) return;
    document.getElementById('ledger-beacon').textContent = formatBytes(bytes);
  }

  // ------------------------------------------------------------------ load

  function load() {
    var win = el.windowSel.value;
    var metric = selectedMetric;
    var q = 'from=' + encodeURIComponent(win);
    var meta = metricByKey[metric];

    setStatus('Loading');
    el.seriesSub.textContent = meta ? meta.name : '';

    Promise.all([
      getJSON('/api/summary?' + q),
      getJSON('/api/series?' + q + '&metric=' + metric + '&n=48'),
      getJSON('/api/routes?' + q + '&metric=' + metric),
      getJSON('/api/devices?' + q + '&metric=' + metric)
    ]).then(function (r) {
      renderScorecard(r[0]);
      renderCounters(r[0]);
      renderBeaconSize(r[0].beaconBytes);
      renderSeries(r[1]);
      renderTable(el.routes, r[2], 'Route', 'No routes reported in this window.');
      renderTable(el.devices, r[3], 'Device', 'No devices reported in this window.');

      var total = r[0].samples;
      setStatus(total.toLocaleString() + (total === 1 ? ' page view' : ' page views'));
    }).catch(function (err) {
      setStatus(err.message || 'Request failed', 'error');
    });
  }

  el.refresh.addEventListener('click', load);
  el.windowSel.addEventListener('change', load);

  buildMetricSelector();
  renderLedger();
  load();
})();

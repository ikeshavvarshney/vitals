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
    journeys: document.getElementById('journeys'),
    journeysSub: document.getElementById('journeys-sub'),
    journeysPrivacy: document.getElementById('journeys-privacy'),
    blame: document.getElementById('blame'),
    navigation: document.getElementById('navigation'),
    storage: document.getElementById('storage'),
    windowSel: document.getElementById('window'),
    metricGroup: document.getElementById('metric-group'),
    pctGroup: document.getElementById('pct-group'),
    scorecardSub: document.getElementById('scorecard-sub'),
    filter: document.getElementById('filter'),
    filterRoute: document.getElementById('filter-route'),
    filterClear: document.getElementById('filter-clear'),
    refresh: document.getElementById('refresh'),
    live: document.getElementById('live'),
    snapLink: document.getElementById('snap-link'),
    copyJSON: document.getElementById('copy-json'),
    downloadJSON: document.getElementById('download-json'),
    copyPrompt: document.getElementById('copy-prompt'),
    exportStatus: document.getElementById('export-status'),
    exportText: document.getElementById('export-text'),
    exportPreview: document.querySelector('.export-preview')
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

  // PERCENTILES matches the set the API accepts. p75 is what Core Web Vitals is
  // assessed on; the others are for looking at the tail a p75 can hide.
  var PERCENTILES = [50, 75, 90, 95];

  var selectedMetric = 'lcp';
  var selectedPercentile = 75;
  var routeFilter = '';

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

  // ordinal labels the selected quantile in prose.
  function ordinal(p) {
    if (p % 10 === 1 && p % 100 !== 11) return p + 'st';
    if (p % 10 === 2 && p % 100 !== 12) return p + 'nd';
    if (p % 10 === 3 && p % 100 !== 13) return p + 'rd';
    return p + 'th';
  }

  // params builds the query string every endpoint shares, so the window, the
  // quantile, and the route filter cannot drift apart between panels.
  function params() {
    var q = 'from=' + encodeURIComponent(el.windowSel.value) +
      '&p=' + selectedPercentile;
    if (routeFilter) q += '&route=' + encodeURIComponent(routeFilter);
    return q;
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

  function buildPercentileSelector() {
    clear(el.pctGroup);

    PERCENTILES.forEach(function (p) {
      var b = h('button', {
        type: 'button',
        role: 'radio',
        'aria-checked': p === selectedPercentile ? 'true' : 'false',
        title: 'Report the ' + ordinal(p) + ' percentile',
        text: 'p' + p
      });
      b.addEventListener('click', function () {
        if (selectedPercentile === p) return;
        selectedPercentile = p;
        buildPercentileSelector();
        load();
      });
      el.pctGroup.appendChild(b);
    });
  }

  // ---------------------------------------------------------- route filter

  // setRoute narrows every panel to one route, or clears the filter when passed
  // an empty string. The breakdown tables stay visible so the filtered state is
  // legible rather than an unexplained drop in every number.
  function setRoute(route) {
    if (routeFilter === route) return;
    routeFilter = route;
    renderFilter();
    load();
  }

  function renderFilter() {
    el.filter.hidden = !routeFilter;
    el.filterRoute.textContent = routeFilter;
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

    if (m.value !== null) {
      var pos = Math.min(100, Math.max(0, (m.value / scaleMax) * 100));
      bar.appendChild(h('div', {
        class: 'track-marker',
        style: 'left:' + pos + '%',
        title: formatValue(m.value, m.unit) + ' against ' +
          formatValue(m.good, m.unit) + ' and ' + formatValue(m.needsImprovement, m.unit)
      }));
    }
    return bar;
  }

  // deltaFor compares a metric with the same figure over the window immediately
  // before this one. Every metric here is better when lower, so a rise is a
  // regression. Movement under 2% is reported as flat: the percentile is
  // bucketed, and a smaller difference can be an artefact of the buckets rather
  // than a change in what visitors experienced.
  function deltaFor(m) {
    if (m.value === null || m.previous === null || m.previous === undefined) {
      return h('span', {
        class: 'delta none',
        title: 'The previous window of the same length holds no samples for this metric',
        text: 'no comparison'
      });
    }

    var pct = ((m.value - m.previous) / m.previous) * 100;
    var state = Math.abs(pct) < 2 ? 'flat' : (pct > 0 ? 'worse' : 'better');
    var sign = pct > 0 ? '+' : (pct < 0 ? '-' : '');
    var text = state === 'flat'
      ? 'flat'
      : sign + Math.abs(pct).toFixed(pct >= 100 ? 0 : 1) + '%';

    return h('span', {
      class: 'delta ' + state,
      title: 'Previous window: ' + formatValue(m.previous, m.unit) +
        unitLabel(m.previous, m.unit) + ' from ' + m.previousSamples +
        (m.previousSamples === 1 ? ' sample' : ' samples'),
      text: text + ' vs before'
    });
  }

  function renderScorecard(data) {
    clear(el.scorecard);

    data.metrics.forEach(function (m) {
      var meta = metricByKey[m.metric] || { label: m.metric, name: '' };
      var value = formatValue(m.value, m.unit);

      var valueNode = value === null
        ? h('div', { class: 'card-value empty', text: 'No data' })
        : h('div', { class: 'card-value' }, [
            h('span', { text: value }),
            h('span', { class: 'unit', text: unitLabel(m.value, m.unit) })
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
        ]),
        h('div', { class: 'card-delta' }, [deltaFor(m)])
      ]));
    });
  }

  // ----------------------------------------------------------------- chart

  var CHART = { w: 940, h: 250, top: 14, right: 14, bottom: 26, left: 54 };

  function renderSeries(data) {
    clear(el.series);
    el.seriesReadout.classList.remove('on');

    var points = data.buckets.filter(function (b) { return b.value !== null; });
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
    var peak = points.reduce(function (a, b) { return Math.max(a, b.value); }, 0);
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
      if (b.value === null) {
        if (run.length) { runs.push(run); run = []; }
        return;
      }
      run.push({ x: x(i), y: y(b.value), b: b });
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
        if (b.value === null) return;
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
        formatDateTime(b.t) + '   ' + formatValue(b.value, data.unit) +
        unitLabel(b.value, data.unit) + '   n=' + b.samples;
      el.seriesReadout.classList.add('on');
    });

    root.addEventListener('pointerleave', function () {
      cursor.setAttribute('opacity', 0);
      el.seriesReadout.classList.remove('on');
    });
  }

  // ---------------------------------------------------------------- tables

  // renderTable draws a breakdown. When onPick is given, each key becomes a
  // button that narrows the whole page to that row.
  function renderTable(table, data, keyHeading, emptyText, onPick) {
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
      h('th', { text: 'p' + selectedPercentile }),
      h('th', { text: 'Relative' }),
      h('th', { text: 'Samples' })
    ])]));

    var worst = data.rows.reduce(function (a, r) {
      return Math.max(a, r.value === null ? 0 : r.value);
    }, 0) || 1;

    var body = h('tbody', {});
    data.rows.forEach(function (row) {
      var value = formatValue(row.value, data.unit);
      var pct = row.value === null ? 0 : Math.max(2, (row.value / worst) * 100);

      var label = row.key || '(unknown)';
      var keyCell;
      if (onPick) {
        var pick = h('button', {
          type: 'button',
          class: 'key-pick',
          title: 'Show only ' + label,
          text: label
        });
        pick.addEventListener('click', function () { onPick(row.key); });
        keyCell = h('td', { class: 'key' }, [pick]);
      } else {
        keyCell = h('td', { class: 'key', text: label });
      }

      body.appendChild(h('tr', {}, [
        keyCell,
        h('td', {
          class: 'num',
          text: value === null ? '-' : value + unitLabel(row.value, data.unit)
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

  // --------------------------------------------------------------- journeys

  // renderJourneys draws one row per visitor: their page views left to right,
  // each coloured by its worst metric, so a visit that got steadily worse reads
  // as a colour gradient rather than as a table to be compared by hand.
  //
  // This is the one view here that follows a person rather than an aggregate.
  // The identifier it shows rotates daily and is never a cookie; the note under
  // the panel is served with the data rather than written into the page, so the
  // disclosure travels with the API response.
  function renderJourneys(data) {
    clear(el.journeys);
    el.journeysPrivacy.textContent = data.note || '';

    var visitors = data.visitors === 1 ? '1 visitor' : data.visitors.toLocaleString() + ' visitors';
    el.journeysSub.textContent = 'What one person actually hit, in order. ' +
      visitors + ' in this window' +
      (data.journeys.length < data.visitors ? ', showing the ' + data.journeys.length + ' most recent' : '') +
      '.';

    if (!data.journeys.length) {
      el.journeys.appendChild(h('p', {
        class: 'empty',
        text: 'No visitors in this window.'
      }));
      return;
    }

    data.journeys.forEach(function (j) {
      var head = [
        h('span', { class: 'journey-id', text: j.session }),
        h('span', {
          class: 'journey-meta',
          text: j.pageViews + (j.pageViews === 1 ? ' page view' : ' page views') +
            (j.durationSeconds >= 1 ? ' over ' + formatDuration(j.durationSeconds) : '')
        })
      ];
      if (j.degraded) {
        head.push(h('span', {
          class: 'journey-flag',
          title: 'The last page view was rated worse than the first',
          text: 'got worse'
        }));
      }

      var steps = h('ol', { class: 'journey-steps' });
      j.steps.forEach(function (step) {
        steps.appendChild(h('li', {
          class: 'journey-step ' + (step.worst || 'none'),
          title: stepTitle(step)
        }, [
          h('span', { class: 'journey-route', text: step.route }),
          h('span', { class: 'journey-figure', text: stepHeadline(step) })
        ]));
      });

      if (j.truncated) {
        steps.appendChild(h('li', {
          class: 'journey-step more',
          text: '+' + (j.pageViews - j.steps.length) + ' more'
        }));
      }

      el.journeys.appendChild(h('div', { class: 'journey' }, [
        h('div', { class: 'journey-head' }, head),
        steps
      ]));
    });
  }

  // stepHeadline picks the one figure worth showing inside a step box: the
  // worst-rated metric, because that is what decided the step's colour.
  function stepHeadline(step) {
    var worstKey = null;
    METRICS.forEach(function (m) {
      if (step.bands[m.key] === step.worst && worstKey === null) worstKey = m.key;
    });
    if (worstKey === null) return '';

    var unit = worstKey === 'cls' ? '' : 'ms';
    var v = step.values[worstKey];
    return m0(worstKey) + ' ' + formatValue(v, unit) + unitLabel(v, unit);
  }

  // m0 returns a metric's short label.
  function m0(key) {
    return (metricByKey[key] || { label: key }).label;
  }

  // stepTitle is the hover text: every metric this page view reported.
  function stepTitle(step) {
    var parts = [step.route, formatClock(step.t)];
    METRICS.forEach(function (m) {
      if (!(m.key in step.values)) return;
      var unit = m.key === 'cls' ? '' : 'ms';
      parts.push(m.label + ' ' + formatValue(step.values[m.key], unit) +
        unitLabel(step.values[m.key], unit) + ' (' + step.bands[m.key] + ')');
    });
    if (step.nav) parts.push(step.nav.replace(/[-_]/g, ' '));
    parts.push(step.device);
    return parts.join(' | ');
  }

  function formatDuration(seconds) {
    if (seconds < 90) return Math.round(seconds) + 's';
    return Math.round(seconds / 60) + 'm';
  }

  // ------------------------------------------------------------ attribution

  // renderBlame lists the element each metric was blamed on, from the report
  // document. Only the full beacon sends attribution, so an empty table here is
  // the normal state for a site running the small one, and the empty text says
  // that rather than implying something failed.
  function renderBlame(report) {
    clear(el.blame);
    el.blame.appendChild(h('caption', { class: 'visually-hidden', text: 'Element blamed per metric' }));

    var rows = [];
    report.metrics.forEach(function (m) {
      (m.offenders || []).forEach(function (o) {
        rows.push({ metric: m.metric, offender: o });
      });
    });

    if (!rows.length) {
      el.blame.appendChild(h('tbody', {}, [
        h('tr', {}, [h('td', {
          class: 'empty',
          colspan: '4',
          text: 'No attribution in this window. The small beacon at /b.js does not report it; ' +
            'switch a page to /b-full.js to see which element is responsible.'
        })])
      ]));
      renderNavigation(report);
      return;
    }

    el.blame.appendChild(h('thead', {}, [h('tr', {}, [
      h('th', { text: 'Metric' }),
      h('th', { text: 'Element' }),
      h('th', { text: 'Named' }),
      h('th', { text: 'Rated poor' })
    ])]));

    var body = h('tbody', {});
    rows.forEach(function (row) {
      var meta = metricByKey[row.metric] || { label: row.metric };
      var o = row.offender;

      body.appendChild(h('tr', {}, [
        h('td', { class: 'key', text: meta.label }),
        // textContent, never innerHTML: the selector is a string the measured
        // page chose, and it is the only field here that a hostile client
        // controls end to end.
        h('td', { class: 'key selector', text: o.selector }),
        h('td', { class: 'num', text: o.samples.toLocaleString() }),
        h('td', {
          class: 'num' + (o.poor > 0 ? ' poor' : ''),
          text: o.poor.toLocaleString()
        })
      ]));
    });
    el.blame.appendChild(body);

    renderNavigation(report);
  }

  // renderNavigation shows how the page views in the window began. A site on
  // the small beacon reports none, so the list is simply left empty.
  function renderNavigation(report) {
    clear(el.navigation);

    (report.navigation || []).forEach(function (n) {
      el.navigation.appendChild(h('div', {}, [
        h('dt', { text: n.type.replace(/[-_]/g, ' ') }),
        h('dd', { text: n.samples.toLocaleString() })
      ]));
    });
  }

  // -------------------------------------------------------------- counters

  function renderCounters(data) {
    clear(el.counters);

    var items = [
      ['Accepted', data.ingest.accepted, false],
      ['Duplicate', data.ingest.duplicate, false],
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

  // --------------------------------------------------------------- storage

  // The tool reports what it costs on disk, measured from the files rather
  // than estimated, for the same reason it reports its own page weight.
  function renderStorage(data) {
    clear(el.storage);
    var c = data.coverage;
    if (!c) return;

    var days = c.files === 1 ? '1 day log' : c.files.toLocaleString() + ' day logs';
    var perRecord = c.bytesPerRecord
      ? c.bytesPerRecord.toFixed(0) + ' B'
      : '-';
    var retention = c.retentionDays
      ? (c.retentionDays >= 1
          ? Math.round(c.retentionDays) + ' days'
          : Math.round(c.retentionDays * 24) + ' hours')
      : 'kept forever';

    var span = '-';
    if (c.oldest && c.newest) {
      span = formatDateTime(c.oldest) + ' to ' + formatDateTime(c.newest);
    }

    [
      ['On disk', formatBytes(c.bytes)],
      ['Files', days],
      ['Per record', perRecord],
      ['Retention', retention],
      ['Span', span]
    ].forEach(function (item) {
      el.storage.appendChild(h('div', {}, [
        h('dt', { text: item[0] }),
        h('dd', { text: item[1] })
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

  // ---------------------------------------------------------- bookmarklet

  // snapshotProgram is the bookmarklet. It is written as an ordinary function
  // and serialised with toString(), so there is one copy of it and it stays
  // readable rather than becoming a hand-escaped string literal.
  //
  // It cannot load a script from this server and it cannot post to it: Chrome
  // blocks requests from a public page to a loopback address unless the page
  // holds a Local Network Access permission it will never be granted. A
  // top-level navigation is exempt, so the program hands its payload to
  // /snapshot.html through the URL fragment, and that page does the POST.
  function snapshotProgram(base) {
    var m = {};
    var observe = function (type, cb, threshold) {
      try {
        new PerformanceObserver(cb).observe({
          type: type, buffered: true, durationThreshold: threshold
        });
      } catch (e) { /* entry type unsupported: metric simply not reported */ }
    };

    observe('navigation', function (l) {
      var e = l.getEntries()[0];
      if (e) m.ttfb = e.responseStart;
    });
    observe('paint', function (l) {
      l.getEntries().forEach(function (e) {
        if (e.name === 'first-contentful-paint') m.fcp = e.startTime;
      });
    });
    observe('largest-contentful-paint', function (l) {
      var e = l.getEntries();
      m.lcp = e[e.length - 1].startTime;
    });

    var wv = 0, ws = 0, wl = 0, worst = 0;
    observe('layout-shift', function (l) {
      l.getEntries().forEach(function (e) {
        if (e.hadRecentInput) return;
        if (wv && e.startTime - wl < 1000 && e.startTime - ws < 5000) {
          wv += e.value; wl = e.startTime;
        } else {
          wv = e.value; ws = wl = e.startTime;
        }
        if (wv > worst) { worst = wv; m.cls = worst; }
      });
    });
    observe('event', function (l) {
      l.getEntries().forEach(function (e) {
        if (e.duration > (m.inp || 0)) m.inp = e.duration;
      });
    }, 16);

    var panel = document.createElement('div');
    panel.setAttribute('role', 'status');
    panel.style.cssText = 'position:fixed;z-index:2147483647;right:12px;bottom:12px;' +
      'width:230px;padding:10px 12px;border-radius:8px;background:#14161c;color:#e7e9ee;' +
      'font:12px/1.5 ui-monospace,Menlo,Consolas,monospace;box-shadow:0 6px 24px rgba(0,0,0,.35)';
    var readout = document.createElement('div');
    var send = document.createElement('button');
    send.textContent = 'Send to vitals';
    send.style.cssText = 'margin-top:8px;width:100%;padding:6px;border:0;border-radius:6px;' +
      'background:#067a52;color:#fff;font:inherit;font-weight:700;cursor:pointer';
    panel.appendChild(readout);
    panel.appendChild(send);
    document.body.appendChild(panel);

    var keys = ['lcp', 'inp', 'cls', 'fcp', 'ttfb'];
    var timer = setInterval(paint, 250);
    paint();

    function paint() {
      readout.textContent = keys.map(function (k) {
        var v = m[k];
        if (v === undefined) return k.toUpperCase() + ' -';
        return k.toUpperCase() + ' ' + (k === 'cls' ? v.toFixed(3) : Math.round(v) + 'ms');
      }).join('  ');
    }

    send.addEventListener('click', function () {
      clearInterval(timer);
      var body = encodeURIComponent(JSON.stringify({
        u: location.host + location.pathname,
        t: Date.now(),
        w: innerWidth,
        m: m
      }));
      panel.remove();
      window.open(base + '/snapshot.html#' + body, '_blank');
    });
  }

  function renderBookmarklet() {
    if (!el.snapLink) return;
    el.snapLink.href =
      'javascript:(' + snapshotProgram.toString() + ')(' +
      JSON.stringify(location.origin) + ')';
    el.snapLink.addEventListener('click', function (ev) {
      // Running it here would measure the dashboard, which is not the point.
      ev.preventDefault();
      el.snapLink.textContent = 'Drag me to the bookmarks bar';
      setTimeout(function () { el.snapLink.textContent = 'vitals snapshot'; }, 1800);
    });
  }

  // ---------------------------------------------------------------- export

  // The report endpoint answers for all five metrics at once, so the export is
  // a separate request rather than a stitching together of what is on screen.
  function fetchReport() {
    return getJSON('/api/report?' + params());
  }

  function exportStatus(text, state) {
    el.exportStatus.textContent = text;
    if (state) el.exportStatus.setAttribute('data-state', state);
    else el.exportStatus.removeAttribute('data-state');
  }

  // showText fills the preview so the copied text is always inspectable, and
  // so there is somewhere to fall back to when the clipboard is unavailable.
  function showText(text, open) {
    el.exportText.value = text;
    if (open) el.exportPreview.open = true;
  }

  // copyText writes to the clipboard. The API is unavailable over plain HTTP on
  // anything but localhost, and can be refused outright, so a failure opens the
  // preview for a manual copy rather than reporting success it did not achieve.
  function copyText(text, label) {
    showText(text, false);

    var writer = navigator.clipboard && navigator.clipboard.writeText
      ? navigator.clipboard.writeText(text)
      : Promise.reject(new Error('clipboard unavailable'));

    writer.then(function () {
      exportStatus(label + ' copied, ' + text.length.toLocaleString() + ' characters');
    }).catch(function () {
      showText(text, true);
      el.exportText.focus();
      el.exportText.select();
      exportStatus('Clipboard refused. The text is selected below; copy it manually.', 'error');
    });
  }

  function copyJSON() {
    exportStatus('Building report');
    fetchReport().then(function (report) {
      copyText(JSON.stringify(report, null, 2), 'Report JSON');
    }).catch(function (err) {
      exportStatus(err.message || 'Request failed', 'error');
    });
  }

  function downloadJSON() {
    exportStatus('Building report');
    fetchReport().then(function (report) {
      var text = JSON.stringify(report, null, 2);
      var url = URL.createObjectURL(new Blob([text], { type: 'application/json' }));
      var name = 'vitals-' + report.generated.slice(0, 19).replace(/[:T]/g, '') + '.json';

      var a = h('a', { href: url, download: name });
      document.body.appendChild(a);
      a.click();
      a.remove();
      // Revoking immediately can race the download in some browsers.
      setTimeout(function () { URL.revokeObjectURL(url); }, 10000);

      showText(text, false);
      exportStatus('Saved as ' + name);
    }).catch(function (err) {
      exportStatus(err.message || 'Request failed', 'error');
    });
  }

  function copyPrompt() {
    exportStatus('Building prompt');
    fetchReport().then(function (report) {
      copyText(buildPrompt(report), 'Prompt');
    }).catch(function (err) {
      exportStatus(err.message || 'Request failed', 'error');
    });
  }

  // reading renders one value the way the prompt should carry it: a number with
  // its unit, or an explicit marker that nothing was measured.
  function reading(v, unit) {
    if (v === null || v === undefined) return '-';
    return formatValue(v, unit) + unitLabel(v, unit);
  }

  // worstOf names the slowest group and its figure, or nothing when a metric
  // has only one group and the comparison would be noise.
  function worstOf(rows, unit) {
    if (!rows || rows.length < 2) return '-';
    return rows[0].key + ' ' + reading(rows[0].value, unit);
  }

  function shortTime(iso) {
    return iso.slice(0, 16).replace('T', ' ') + 'Z';
  }

  // buildPrompt turns a report into text an agent can act on. It is deliberately
  // compact: a page of prose gets skimmed, and a long paste is awkward to move
  // between tools. One line per metric, the caveats in one line, and a short
  // instruction that says what the data cannot answer.
  function buildPrompt(report) {
    var lines = [];
    var pk = 'p' + Math.round(report.headlinePercentile * 100);

    lines.push('Core Web Vitals field data from my site, real page views, collected by a ' +
      'self-hosted RUM tool. Tell me what to fix, in order.');
    lines.push('');
    lines.push(shortTime(report.from) + ' to ' + shortTime(report.to) + ', ' +
      Math.round(report.windowHours) + 'h, ' + report.pageViews + ' page views' +
      (report.route ? ', route ' + report.route + ' only' : '') + '. ' +
      'Headline figures are ' + pk + '. Targets are the published thresholds.');
    lines.push('');
    lines.push('metric | p50 | ' + pk + ' | p95 | worst | rating | target | good/ni/poor | worst route | worst device');

    report.metrics.forEach(function (m) {
      var u = m.unit;
      if (!m.samples) {
        lines.push(m.metric.toUpperCase() + ' | no samples in this window (absent, not fast)');
        return;
      }
      var q = m.quantiles || {};
      var d = m.distribution;
      lines.push([
        m.metric.toUpperCase(),
        reading(q.p50, u),
        reading(q[pk], u),
        reading(q.p95, u),
        reading(m.max, u),
        m.band || '-',
        '<=' + reading(m.good, u),
        d.good + '/' + d.needsImprovement + '/' + d.poor + ' of ' + m.samples,
        worstOf(m.worstRoutes, u),
        worstOf(m.worstDevices, u)
      ].join(' | '));
    });

    lines.push('');
    lines.push('Caveats: percentiles are bucketed, not exact (+/-4.9% on ms metrics, ' +
      '+/-0.0025 on CLS); band counts are exact; INP here is the longest event over ' +
      '16ms, not real INP, so it is pessimistic; device class comes from viewport ' +
      'width, not the user agent.');
    lines.push('');
    lines.push('Answer with: the two or three metrics worth working on and what in these ' +
      'numbers says so; causes consistent with the split across routes, devices and ' +
      'percentiles, separating what the data supports from what it merely permits; ' +
      'changes ranked by effect against effort, each naming the metric it should move. ' +
      'This is field data only: no waterfall, no resource list, no element attribution. ' +
      'Do not guess my stack, ask. If the samples are too few to support a conclusion, ' +
      'say that instead of drawing one.');

    return lines.join('\n');
  }

  // ------------------------------------------------------------------ live

  // The dashboard subscribes to a Server-Sent Events stream and reloads when a
  // measurement arrives. It does not poll: an idle instance costs one open
  // connection and a keep-alive comment every 25 seconds.
  //
  // Reloads are coalesced. A burst of page views would otherwise fire a burst
  // of four API requests each, and the figures cannot meaningfully change
  // faster than a person can read them.
  var LIVE_QUIET_MS = 1500;

  var live = { source: null, pending: null, since: 0, seen: 0 };

  function liveState(state, detail) {
    if (!el.live) return;
    el.live.setAttribute('data-state', state);
    el.live.textContent = detail;
  }

  function startLive() {
    if (!window.EventSource || live.source) return;

    var source = new EventSource('/api/events');
    live.source = source;

    source.addEventListener('open', function () {
      liveState('on', 'Live');
    });

    source.addEventListener('sample', function () {
      live.seen++;
      liveState('on', live.seen === 1 ? '1 arrived' : live.seen + ' arrived');

      if (live.pending) clearTimeout(live.pending);
      live.pending = setTimeout(function () {
        live.pending = null;
        load();
      }, LIVE_QUIET_MS);
    });

    source.addEventListener('error', function () {
      // EventSource reconnects on its own; say so rather than implying loss.
      liveState('off', 'Reconnecting');
    });
  }

  // ------------------------------------------------------------------ load

  function load() {
    var metric = selectedMetric;
    var q = params();
    var meta = metricByKey[metric];

    setStatus('Loading');
    el.seriesSub.textContent = meta ? meta.name : '';
    document.getElementById('open-json').href = '/api/report?' + q;
    el.scorecardSub.textContent = ordinal(selectedPercentile) +
      ' percentile across the selected window' +
      (routeFilter ? ', for ' + routeFilter : '');

    Promise.all([
      getJSON('/api/summary?' + q),
      getJSON('/api/series?' + q + '&metric=' + metric + '&n=48'),
      getJSON('/api/routes?' + q + '&metric=' + metric),
      getJSON('/api/devices?' + q + '&metric=' + metric),
      // The report document is the only one carrying attribution. It is a
      // fifth request rather than a sixth field on the summary because it is
      // the same document the export already builds, so there is one place
      // where a metric's offenders are computed.
      fetchReport(),
      getJSON('/api/journeys?' + q)
    ]).then(function (r) {
      renderScorecard(r[0]);
      renderCounters(r[0]);
      renderStorage(r[0]);
      renderBeaconSize(r[0].beaconBytes);
      renderSeries(r[1]);
      // The route table stays pickable while filtered, so a wrong pick is one
      // click from a different one rather than a trip through Clear filter.
      renderTable(el.routes, r[2], 'Route', 'No routes reported in this window.', setRoute);
      renderTable(el.devices, r[3], 'Device', 'No devices reported in this window.');
      renderBlame(r[4]);
      renderJourneys(r[5]);

      var total = r[0].samples;
      setStatus(total.toLocaleString() + (total === 1 ? ' page view' : ' page views'));
    }).catch(function (err) {
      setStatus(err.message || 'Request failed', 'error');
    });
  }

  el.refresh.addEventListener('click', load);
  el.windowSel.addEventListener('change', function () {
    exportStatus('');
    load();
  });
  el.filterClear.addEventListener('click', function () { setRoute(''); });
  el.copyJSON.addEventListener('click', copyJSON);
  el.downloadJSON.addEventListener('click', downloadJSON);
  el.copyPrompt.addEventListener('click', copyPrompt);

  buildMetricSelector();
  buildPercentileSelector();
  startLive();
  renderFilter();
  renderLedger();
  renderBookmarklet();
  load();
})();

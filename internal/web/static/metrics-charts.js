/* Progressive charts for the instance-metrics screen.
 *
 * The text summary beside each chart is the complete rendering; this
 * module reads the series the server already put in the document, draws
 * it with uPlot, and refreshes it with a same-origin poll of the series
 * endpoint. It fetches nothing outside the catalog and writes nothing
 * back; a failed poll leaves the last drawn data in place.
 */

/* The charts were 190px tall, which is a sparkline's height, not a
   chart's: two instances tracking each other were a single thick line.
   A panel you actually read a trend off wants room, so this is sized
   like one rather than like a thumbnail. */
var CHART_HEIGHT = 480;

/* The values follow the cursor instead of sitting under the chart. The
   legend stays, stripped back to a colour key: which line is which is a
   question you have before you hover, so it must be answerable without
   hovering. The readings are the part that belongs at the pointer.
   uPlot ships no tooltip, so this is a small plugin over its cursor
   hooks — no extra dependency, and it draws into u.over, which is the
   positioned box uPlot already maintains. */
/* The palette is passed in rather than read back off the series: uPlot
   normalises series.stroke into a function, so asking the chart for its
   own colour returns something unusable in a style attribute. */
function tooltipPlugin(colors) {
  var tip;
  function fmtTime(ts) {
    var d = new Date(ts * 1000);
    return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }
  return {
    hooks: {
      /* Built on first use rather than from a lifecycle hook: which of
         those fires is a uPlot version detail, and the cursor callback
         is the only one this needs to be correct about. */
      setCursor: function (u) {
        if (!tip) {
          tip = document.createElement('div');
          tip.className = 'metric-tip';
          tip.hidden = true;
          u.over.appendChild(tip);
          u.over.addEventListener('mouseleave', function () { tip.hidden = true; });
        }
        var idx = u.cursor.idx;
        if (idx == null || u.cursor.left < 0) { tip.hidden = true; return; }
        var rows = '<div class="metric-tip-time">' + fmtTime(u.data[0][idx]) + '</div>';
        var any = false;
        for (var i = 1; i < u.series.length; i++) {
          var v = u.data[i][idx];
          if (v == null) continue;      /* a gap reports nothing, not zero */
          any = true;
          rows += '<div class="metric-tip-row">' +
            '<span class="metric-tip-key"></span>' +
            '<span class="metric-tip-name"></span>' +
            '<span class="metric-tip-val"></span></div>';
        }
        if (!any) { tip.hidden = true; return; }
        tip.innerHTML = rows;
        /* Names and values are set as text, never interpolated into the
           markup above, so an instance name cannot carry markup in. The
           swatch colour is assigned through the CSSOM for the same kind
           of reason: the policy forbids a style attribute in parsed
           markup, and a property assignment is not one. */
        var keys = tip.querySelectorAll('.metric-tip-key');
        var names = tip.querySelectorAll('.metric-tip-name');
        var vals = tip.querySelectorAll('.metric-tip-val');
        var n = 0;
        for (var j = 1; j < u.series.length; j++) {
          if (u.data[j][idx] == null) continue;
          keys[n].style.background = colors[(j - 1) % colors.length];
          names[n].textContent = u.series[j].label;
          vals[n].textContent = u.data[j][idx];
          n++;
        }
        tip.hidden = false;
        /* Flip to the other side of the cursor near the right edge, so
           the tip never leaves the plot. */
        var w = tip.offsetWidth, h = tip.offsetHeight;
        var left = u.cursor.left + 14;
        if (left + w > u.over.clientWidth) left = u.cursor.left - w - 14;
        var top = u.cursor.top - h / 2;
        if (top < 0) top = 0;
        if (top + h > u.over.clientHeight) top = u.over.clientHeight - h;
        tip.style.transform = 'translate(' + Math.round(left) + 'px,' + Math.round(top) + 'px)';
      }
    }
  };
}


/* The store already keeps a rollup tier spanning the whole retention,
   and the series endpoint already answers for it — the chart simply
   never asked. The selector is script-only because the chart is: with
   no script there is no chart to rescale, and the table beside it
   carries the numbers regardless. */
function windowSelect() {
  return document.querySelector('[data-metrics-window-select]');
}
function activeWindow() {
  var sel = windowSelect();
  return sel && sel.value === 'retention' ? 'retention' : 'raw';
}
(function () {
  'use strict';

  var POLL_MS = 10000;
  var charts = [];

  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  function palette() {
    return [cssVar('--accent'), cssVar('--ok'), cssVar('--warn'), cssVar('--bad'), cssVar('--text-muted')];
  }

  function toData(payload) {
    var data = [payload.times];
    (payload.instances || []).forEach(function (col) {
      data.push(col.values);
    });
    return data;
  }

  function build(container) {
    if (!window.uPlot || container.dataset.metricReady === 'true') return;
    var payload;
    try {
      payload = JSON.parse(container.dataset.metricSeries);
    } catch (e) {
      return; // The text summary stays the rendering.
    }
    if (!payload.times || !payload.times.length) return;

    var colors = palette();
    var series = [{}];
    (payload.instances || []).forEach(function (col, index) {
      series.push({
        label: col.name,
        stroke: colors[index % colors.length],
        width: 1.5,
        spanGaps: false,
        points: { show: false }
      });
    });

    var opts = {
      width: Math.max(container.clientWidth, 320),
      height: CHART_HEIGHT,
      series: series,
      axes: [
        { stroke: cssVar('--text-muted'), grid: { stroke: cssVar('--border'), width: 1 }, ticks: { stroke: cssVar('--border') } },
        { stroke: cssVar('--text-muted'), grid: { stroke: cssVar('--border'), width: 1 }, ticks: { stroke: cssVar('--border') } }
      ],
      /* A live legend is the readout: hovering a time reports every
         series' value at that instant, which is the question a reader
         has when two instances diverge. It was off, so the chart could
         be seen but not read. */
      legend: { live: false },
      plugins: [tooltipPlugin(colors)],
      /* Drag across the plot to zoom the time axis, double-click to
         return to the whole window — the interaction anyone who has
         used a dashboard already knows. It was explicitly disabled
         (setScale:false), which is why nothing happened. Only x
         rescales: the y axis is the reading, and zooming it hides the
         magnitude the chart exists to show. */
      cursor: {
        drag: { x: true, y: false, setScale: true },
        focus: { prox: 1e6 }
      },
      focus: { alpha: 1 },
      hooks: { setScale: [function () { syncRangeInputs(); }] }
    };

    /* Revealed before construction: a hidden element reports
       clientWidth 0, so measuring it here handed uPlot the 320px floor
       and every chart drew a fifth of its container wide, forever —
       nothing re-measures until a window resize. */
    container.hidden = false;
    container.removeAttribute('aria-hidden');
    opts.width = Math.max(container.clientWidth, 320);
    var chart = new uPlot(opts, toData(payload), container);
    var entry = { chart: chart, container: container, key: container.dataset.metricKey };
    charts.push(entry);
    container.dataset.metricReady = 'true';
    chart.hooks = chart.hooks || {};
    syncRangeInputs();
  }

  /* Grafana's bargain: a drag zooms, and the same range is also
     typeable. Both act on the data already fetched — the selector above
     decides how much is fetched, these decide how much of it is shown,
     which is why applying a range never waits on the network. */
  function rangeInputs() {
    return {
      from: document.querySelector('[data-metrics-from]'),
      to: document.querySelector('[data-metrics-to]')
    };
  }
  function toLocalInput(sec) {
    var d = new Date(sec * 1000);
    var pad = function (n) { return String(n).padStart(2, '0'); };
    return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) +
      'T' + pad(d.getHours()) + ':' + pad(d.getMinutes());
  }
  function applyRange() {
    var io = rangeInputs();
    if (!io.from || !io.to) return;
    var min = Date.parse(io.from.value) / 1000;
    var max = Date.parse(io.to.value) / 1000;
    if (!isFinite(min) || !isFinite(max) || min >= max) return;
    charts.forEach(function (entry) { entry.chart.setScale('x', { min: min, max: max }); });
  }
  function resetRange() {
    charts.forEach(function (entry) {
      var t = entry.chart.data[0];
      if (!t || !t.length) return;
      entry.chart.setScale('x', { min: t[0], max: t[t.length - 1] });
    });
    syncRangeInputs();
  }
  /* The boxes show the window actually plotted, so a drag-zoom writes
     itself back into them rather than leaving them stale. */
  function syncRangeInputs() {
    var io = rangeInputs();
    if (!io.from || !io.to || !charts.length) return;
    var sc = charts[0].chart.scales.x;
    if (sc.min == null || sc.max == null) return;
    io.from.value = toLocalInput(sc.min);
    io.to.value = toLocalInput(sc.max);
  }

  function poll() {
    /* The background cadence skips a hidden tab; an explicit request
       for a different window does not, so the two are separate. */
    if (document.hidden) return;
    refresh();
  }

  function refresh() {
    charts.forEach(function (entry) {
      if (!document.body.contains(entry.container)) return;
      fetch('/cluster/metrics/series?key=' + encodeURIComponent(entry.key) +
            '&window=' + encodeURIComponent(activeWindow()), {
        credentials: 'same-origin'
      }).then(function (resp) {
        if (!resp.ok) throw new Error('status');
        return resp.json();
      }).then(function (payload) {
        if (!document.body.contains(entry.container)) return;
        entry.chart.setData(toData(payload));
      }).catch(function () {
        /* The last drawn data stays; the text summary refreshes with
           the page's own refresh cycle. */
      });
    });
  }

  function resize() {
    charts.forEach(function (entry) {
      if (!document.body.contains(entry.container)) return;
      entry.chart.setSize({ width: Math.max(entry.container.clientWidth, 320), height: CHART_HEIGHT });
    });
  }

  /* The control is inert without this module, so it stays hidden until
     the module is running — the same rule the charts follow. */
  function revealWindowControl() {
    var box = document.querySelector('[data-metrics-window]');
    if (!box || box.dataset.metricsWindowReady) return;
    box.dataset.metricsWindowReady = '1';
    box.hidden = false;
    box.removeAttribute('aria-hidden');
    var sel = windowSelect();
    if (sel) sel.addEventListener('change', refresh);
    var apply = document.querySelector('[data-metrics-apply]');
    var reset = document.querySelector('[data-metrics-reset]');
    if (apply) apply.addEventListener('click', applyRange);
    if (reset) reset.addEventListener('click', resetRange);
  }

  function rebuild() {
    charts.forEach(function (entry) { entry.chart.destroy(); });
    charts = [];
    Array.prototype.forEach.call(document.querySelectorAll('[data-metric-chart]'), function (el) {
      delete el.dataset.metricReady;
      el.replaceChildren();
      build(el);
    });
  }

  function start(root) {
    revealWindowControl();
    var scope = root && root.querySelectorAll ? root : document;
    // Drop chart entries whose containers a swap removed.
    charts = charts.filter(function (entry) {
      if (document.body.contains(entry.container)) return true;
      entry.chart.destroy();
      return false;
    });
    Array.prototype.forEach.call(scope.querySelectorAll('[data-metric-chart]'), build);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { start(document); });
  } else {
    start(document);
  }
  document.addEventListener('htmx:afterSwap', function (event) {
    start(event.detail && event.detail.elt);
  });
  // The theme swap changes every colour the charts sampled.
  new MutationObserver(rebuild).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  window.addEventListener('resize', resize);
  window.setInterval(poll, POLL_MS);
})();

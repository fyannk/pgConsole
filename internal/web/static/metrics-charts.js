/* Progressive charts for the instance-metrics screen.
 *
 * The text summary beside each chart is the complete rendering; this
 * module reads the series the server already put in the document, draws
 * it with uPlot, and refreshes it with a same-origin poll of the series
 * endpoint. It fetches nothing outside the catalog and writes nothing
 * back; a failed poll leaves the last drawn data in place.
 */
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
      height: 190,
      series: series,
      axes: [
        { stroke: cssVar('--text-muted'), grid: { stroke: cssVar('--border'), width: 1 }, ticks: { stroke: cssVar('--border') } },
        { stroke: cssVar('--text-muted'), grid: { stroke: cssVar('--border'), width: 1 }, ticks: { stroke: cssVar('--border') } }
      ],
      legend: { live: false },
      cursor: { drag: { setScale: false } }
    };

    container.hidden = false;
    var chart = new uPlot(opts, toData(payload), container);
    var entry = { chart: chart, container: container, key: container.dataset.metricKey };
    charts.push(entry);
    container.dataset.metricReady = 'true';
  }

  function poll() {
    if (document.hidden) return;
    charts.forEach(function (entry) {
      if (!document.body.contains(entry.container)) return;
      fetch('/cluster/metrics/series?key=' + encodeURIComponent(entry.key), {
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
      entry.chart.setSize({ width: Math.max(entry.container.clientWidth, 320), height: 190 });
    });
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

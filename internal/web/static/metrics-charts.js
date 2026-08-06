/* Progressive charts for the instance-metrics screen.
 *
 * The text beside each chart is the complete rendering; this module
 * draws the same window with uPlot and refreshes it from a same-origin
 * poll of the series endpoint. It fetches nothing outside the catalog
 * and writes nothing back; a failed fetch leaves the text summary as
 * the whole reading, and a failed refresh leaves the last drawn data.
 *
 * Two things changed when the screen grew to a full catalog on every
 * tab, and both are about not doing work nobody asked for:
 *
 *   - the series are fetched, not inlined. Inlining every instrument's
 *     raw window for every tab put megabytes of numbers in the document,
 *     nearly all of them for tabs that were never opened.
 *   - a chart is built when it is both on the visible tab and near the
 *     viewport. Constructing a hundred canvases up front is seconds of
 *     main thread for panels below the fold.
 *
 * Neither weakens the no-script contract: the window summary is served
 * as text beside every chart, on every tab, before this file runs.
 */

/* Tall enough that two instances tracking each other are two lines
   rather than one thick one, short enough that a section of eight
   panels is still scannable. The panels sit two-up on a wide screen,
   which is where the rest of the vertical budget went. */
var CHART_HEIGHT = 280;

/* How far outside the viewport a chart starts loading. One screen of
   lead time means scrolling rarely arrives before the data does. */
var PRELOAD_MARGIN = '600px';

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
    var d = tzDate(ts);
    return fmtDate(d) + ' ' + fmtClock(d, true) + (utcPreferred() ? 'Z' : '');
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
        var rows = '<div class="metric-tip-time"></div>';
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
        /* Names, values and the time are set as text, never interpolated
           into the markup above, so an instance name cannot carry markup
           in. The swatch colour is assigned through the CSSOM for the
           same kind of reason: the policy forbids a style attribute in
           parsed markup, and a property assignment is not one. */
        tip.querySelector('.metric-tip-time').textContent = fmtTime(u.data[0][idx]);
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

/* The axis and the cursor must agree with the rest of the page about
   which zone a time is in. console.js owns that preference; this reads
   it, and rebuilds every chart when it changes. */
function utcPreferred() {
  try {
    return window.localStorage.getItem('pgconsole.timezone') === 'utc';
  } catch (e) {
    return false;
  }
}

/* uPlot formats axis ticks with the local Date accessors. Handing it a
   Date shifted by the zone offset is the documented way to make those
   accessors read UTC instead. */
function tzDate(ts) {
  var d = new Date(ts * 1000);
  if (!utcPreferred()) return d;
  return new Date(ts * 1000 + d.getTimezoneOffset() * 60000);
}

/* uPlot's built-in axis formats are template strings with a hardcoded
   {aa}: 12-hour with am/pm, whatever the reader is. That was the first
   half of the problem. The second half is that the console renders every
   other absolute time as YYYY-MM-DD HH:MM:SS — the server does, the
   tiles do, the timelines do — so deferring the axis to Intl would have
   traded one inconsistency for another: on a browser running in English
   the ticks would still have read 12-hour beside a page that never does.

   So the axis, the cursor and the zoom boxes all use the console's own
   format. It is 24-hour, it sorts, it is the same in every locale, and
   it is the one already on screen everywhere else. The zone it is
   rendered in still follows the top bar's toggle. */
function pad(n) { return String(n).padStart(2, '0'); }
function fmtDate(d) { return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()); }
function fmtClock(d, withSeconds) {
  return pad(d.getHours()) + ':' + pad(d.getMinutes()) +
    (withSeconds ? ':' + pad(d.getSeconds()) : '');
}

/* uPlot hands the axis its splits in seconds plus the tick increment it
   settled on, which is what picks the granularity: seconds when the
   ticks are seconds apart, a bare date when they are days apart. */
function axisValues(u, splits, axisIdx, foundSpace, foundIncr) {
  var daily = foundIncr >= 86400;
  var seconds = foundIncr < 60;
  var previousDay = null;
  return splits.map(function (ts, i) {
    var d = tzDate(ts);
    if (daily) return fmtDate(d);
    var label = fmtClock(d, seconds);
    /* Below a day the ticks are clock times, so a window spanning
       midnight would otherwise run 23:59 straight into 00:00 with no
       sign the day turned. Only the crossing tick carries a date, and
       it carries the short form: uPlot spaces ticks by the width it
       expects them to have, so a full 2026-08-06 10:42:40 on the first
       tick simply overlapped the second. The full date is a hover
       away, in the cursor readout. */
    var day = d.getDate();
    if (previousDay !== null && day !== previousDay) {
      label = pad(d.getMonth() + 1) + '-' + pad(day) + ' ' + label;
    }
    previousDay = day;
    return label;
  });
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
  var observer = null;

  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  function palette() {
    return [cssVar('--accent'), cssVar('--ok'), cssVar('--warn'), cssVar('--bad'), cssVar('--text-muted')];
  }

  function toData(payload) {
    var data = [payload.times || []];
    (payload.instances || []).forEach(function (col) {
      data.push(col.values);
    });
    return data;
  }

  /* A chart on a hidden tab measures zero wide and must not be built:
     uPlot would take the floor width and keep it, since nothing
     re-measures until a resize. */
  function onVisibleTab(container) {
    var tab = container.closest('.metrics-tab');
    return !tab || !tab.hidden;
  }

  function seriesURL(container) {
    var url = '/cluster/metrics/series?key=' + encodeURIComponent(container.dataset.metricKey) +
      '&window=' + encodeURIComponent(activeWindow());
    if (container.dataset.metricInstance) {
      url += '&instance=' + encodeURIComponent(container.dataset.metricInstance);
    }
    return url;
  }

  /* Fetches this panel's window and draws it. Marked pending before the
     request so a scroll that re-triggers the observer does not queue a
     second fetch for the same panel. */
  function build(container) {
    if (!window.uPlot) return;
    if (container.dataset.metricReady || container.dataset.metricPending) return;
    if (!onVisibleTab(container)) return;
    container.dataset.metricPending = '1';

    fetch(seriesURL(container), { credentials: 'same-origin' }).then(function (resp) {
      if (!resp.ok) throw new Error('status');
      return resp.json();
    }).then(function (payload) {
      delete container.dataset.metricPending;
      if (!document.body.contains(container)) return;
      if (!payload.times || !payload.times.length) return;
      draw(container, payload);
    }).catch(function () {
      delete container.dataset.metricPending;
      /* The text summary beside this panel stays the whole reading. */
    });
  }

  function draw(container, payload) {
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
      width: 320,
      height: CHART_HEIGHT,
      series: series,
      tzDate: tzDate,
      axes: [
        {
          stroke: cssVar('--text-muted'), grid: { stroke: cssVar('--border'), width: 1 },
          ticks: { stroke: cssVar('--border') }, values: axisValues
        },
        { stroke: cssVar('--text-muted'), grid: { stroke: cssVar('--border'), width: 1 }, ticks: { stroke: cssVar('--border') } }
      ],
      /* A live legend is the readout: hovering a time reports every
         series' value at that instant, which is the question a reader
         has when two instances diverge. */
      legend: { live: false },
      scales: {
        /* uPlot's default range for a series that never moves is 0 to
           100, which draws a flat line along the bottom of a scale
           nothing in the data justifies — and most of these series are
           legitimately flat at zero most of the time. A metric pinned at
           zero should read as zero, not as "0 out of 100". */
        y: {
          range: function (u, dataMin, dataMax) {
            if (dataMin == null || dataMax == null) return [0, 1];
            if (dataMin === dataMax) return dataMin === 0 ? [0, 1] : [dataMin * 0.9, dataMax * 1.1];
            return uPlot.rangeNum(dataMin, dataMax, 0.1, true);
          }
        }
      },
      plugins: [tooltipPlugin(colors)],
      /* Drag across the plot to zoom the time axis, double-click to
         return to the whole window — the interaction anyone who has
         used a dashboard already knows. Only x rescales: the y axis is
         the reading, and zooming it hides the magnitude the chart
         exists to show. */
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
    charts.push({ chart: chart, container: container, key: container.dataset.metricKey });
    container.dataset.metricReady = 'true';
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
  /* <input type="datetime-local"> was the obvious control and the wrong
     one: Chromium renders it in the browser's own UI locale and ignores
     both the document's lang and the element's, so on an
     English-language browser it read "08/06/2026, 10:37 AM" beside a
     page that states every other time as 2026-08-06 10:37:01. There is
     no attribute that changes that. A plain text box in the console's
     own format is fully under our control and matches everything around
     it; the primary way to zoom is dragging across a chart anyway. */
  var INPUT_PATTERN = /^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})$/;

  function toInputValue(sec) {
    var d = tzDate(sec);
    return fmtDate(d) + ' ' + fmtClock(d, false);
  }

  /* Parsed in the zone the page is currently showing, so a value typed
     back into the box means what it looked like when it was written. */
  function fromInputValue(text) {
    var m = INPUT_PATTERN.exec((text || '').trim());
    if (!m) return NaN;
    var y = +m[1], mo = +m[2] - 1, day = +m[3], hh = +m[4], mm = +m[5];
    var ms = utcPreferred() ? Date.UTC(y, mo, day, hh, mm) : new Date(y, mo, day, hh, mm).getTime();
    return ms / 1000;
  }

  function visible() {
    return charts.filter(function (entry) {
      return document.body.contains(entry.container) && onVisibleTab(entry.container);
    });
  }
  function applyRange() {
    var io = rangeInputs();
    if (!io.from || !io.to) return;
    var min = fromInputValue(io.from.value);
    var max = fromInputValue(io.to.value);
    var bad = !isFinite(min) || !isFinite(max) || min >= max;
    /* Say so rather than doing nothing: a typo in a text box that
       silently no-ops is indistinguishable from a broken button. */
    [io.from, io.to].forEach(function (input) {
      input.setAttribute('aria-invalid', bad ? 'true' : 'false');
    });
    if (bad) return;
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
    if (!io.from || !io.to) return;
    var shown = visible();
    if (!shown.length) return;
    var sc = shown[0].chart.scales.x;
    if (sc.min == null || sc.max == null) return;
    io.from.value = toInputValue(sc.min);
    io.to.value = toInputValue(sc.max);
    io.from.setAttribute('aria-invalid', 'false');
    io.to.setAttribute('aria-invalid', 'false');
  }

  function poll() {
    /* The background cadence skips a hidden tab; an explicit request
       for a different window does not, so the two are separate. */
    if (document.hidden) return;
    refresh();
  }

  /* Only the charts on the visible tab refresh. The others keep their
     last drawn data and catch up when their tab is shown, which is one
     fetch per panel rather than one per panel per tab per poll. */
  function refresh() {
    visible().forEach(function (entry) {
      fetch(seriesURL(entry.container), { credentials: 'same-origin' }).then(function (resp) {
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
    visible().forEach(function (entry) {
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
    if (sel) sel.addEventListener('change', function () { rebuild(); });
    var apply = document.querySelector('[data-metrics-apply]');
    var reset = document.querySelector('[data-metrics-reset]');
    if (apply) apply.addEventListener('click', applyRange);
    if (reset) reset.addEventListener('click', resetRange);
  }

  /* Everything drawn is discarded and the visible tab redrawn from
     scratch. Used when the window selector, the theme or the time zone
     changes — each of those invalidates data, colours or axis labels
     that uPlot captured at construction. */
  function rebuild() {
    charts.forEach(function (entry) { entry.chart.destroy(); });
    charts = [];
    Array.prototype.forEach.call(document.querySelectorAll('[data-metric-chart]'), function (el) {
      delete el.dataset.metricReady;
      delete el.dataset.metricPending;
      el.replaceChildren();
      el.hidden = true;
      el.setAttribute('aria-hidden', 'true');
      delete panelOf(el).dataset.metricObserved;
    });
    observer = null;
    scan(document);
  }

  /* What the observer watches is the panel, never the chart box itself.
     The chart box is served hidden — the text beside it is the whole
     rendering until this module has something to draw — and a
     display:none element has no geometry, so it never intersects
     anything and the observer would sit silent forever. The panel is
     always laid out, and it is where the chart will go. */
  function panelOf(container) {
    return container.closest('.metric-panel') || container.parentElement || container;
  }

  /* Registers every unbuilt panel with the observer, which builds its
     chart as the panel approaches the viewport. Panels on a hidden tab
     are registered too and simply decline to build until their tab is
     shown; showTab then sweeps the ones that landed on screen. */
  function scan(root) {
    var scope = root && root.querySelectorAll ? root : document;
    if (!observer) {
      if (!window.IntersectionObserver) {
        /* No observer: build what is on the visible tab and rely on the
           tab handler for the rest. */
        Array.prototype.forEach.call(scope.querySelectorAll('[data-metric-chart]'), build);
        return;
      }
      observer = new IntersectionObserver(function (entries) {
        entries.forEach(function (entry) {
          if (!entry.isIntersecting) return;
          var chart = entry.target.querySelector('[data-metric-chart]');
          if (chart) build(chart);
        });
      }, { rootMargin: PRELOAD_MARGIN });
    }
    Array.prototype.forEach.call(scope.querySelectorAll('[data-metric-chart]'), function (el) {
      var panel = panelOf(el);
      if (panel.dataset.metricObserved) return;
      panel.dataset.metricObserved = '1';
      observer.observe(panel);
    });
  }

  /* A tab that has just been shown has panels the observer reported as
     intersecting while the tab was hidden, and it will not report them
     again. So build the ones now on screen directly, and resize the
     ones that were drawn before. */
  function showTab() {
    Array.prototype.forEach.call(document.querySelectorAll('[data-metric-chart]'), function (el) {
      if (el.dataset.metricReady || !onVisibleTab(el)) return;
      var box = panelOf(el).getBoundingClientRect();
      if (box.top < window.innerHeight + 600 && box.bottom > -600) build(el);
    });
    resize();
    syncRangeInputs();
  }

  function start(root) {
    revealWindowControl();
    // Drop chart entries whose containers a swap removed.
    charts = charts.filter(function (entry) {
      if (document.body.contains(entry.container)) return true;
      entry.chart.destroy();
      return false;
    });
    scan(root);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { start(document); });
  } else {
    start(document);
  }
  document.addEventListener('htmx:afterSwap', function (event) {
    observer = null; // the swapped-away nodes took their registrations with them
    start(event.detail && event.detail.elt);
  });
  /* console.js announces both of these after it has done its own work. */
  document.addEventListener('pgconsole:tabshown', function () { window.setTimeout(showTab, 0); });
  document.addEventListener('pgconsole:timezone', rebuild);
  // The theme swap changes every colour the charts sampled.
  new MutationObserver(rebuild).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  window.addEventListener('resize', resize);
  window.setInterval(poll, POLL_MS);
})();

/* Progressive visual timeline for the bounded semantic history table.
 *
 * The table is the complete, accessible rendering. This module reads at most
 * forty of its already-rendered rows and draws an aria-hidden SVG overview;
 * it fetches nothing and makes no claim the server did not already print.
 */
(function () {
  'use strict';

  var SVGNS = 'http://www.w3.org/2000/svg';
  var MAX_POINTS = 40;

  function svgElement(name, attributes, parent) {
    var node = document.createElementNS(SVGNS, name);
    Object.keys(attributes || {}).forEach(function (key) {
      node.setAttribute(key, attributes[key]);
    });
    if (parent) parent.appendChild(node);
    return node;
  }

  function build(container) {
    if (!container || container.dataset.historyReady === 'true') return;
    var panel = container.closest('.panel');
    if (!panel) return;
    var rows = Array.prototype.slice.call(panel.querySelectorAll('tr[data-history-seq]'), 0, MAX_POINTS);
    if (!rows.length) return;
    rows.reverse();

    var width = 900;
    var height = 112;
    var left = 34;
    var right = width - 34;
    var y = 54;
    var svg = svgElement('svg', {
      viewBox: '0 0 ' + width + ' ' + height,
      preserveAspectRatio: 'xMidYMid meet'
    }, container);
    svgElement('line', { x1: left, y1: y, x2: right, y2: y, 'class': 'history-axis' }, svg);

    rows.forEach(function (row, index) {
      var x = rows.length === 1 ? width / 2 : left + (right - left) * index / (rows.length - 1);
      var change = row.getAttribute('data-history-change') || 'unknown';
      var seq = row.getAttribute('data-history-seq') || '';
      var group = svgElement('g', { 'class': 'history-point history-point-' + change }, svg);
      svgElement('circle', { cx: Math.round(x), cy: y, r: 6 }, group);
      if (index === 0 || index === rows.length - 1 || index % 8 === 0) {
        var label = svgElement('text', { x: Math.round(x), y: y + 25, 'text-anchor': 'middle' }, group);
        label.textContent = 'r' + seq;
      }
    });

    var caption = svgElement('text', { x: left, y: 18, 'class': 'history-visual-label' }, svg);
    caption.textContent = 'Oldest to newest within this retained page';
    container.hidden = false;
    container.dataset.historyReady = 'true';
  }

  function start(root) {
    var scope = root && root.querySelectorAll ? root : document;
    Array.prototype.forEach.call(scope.querySelectorAll('[data-history-visual]'), build);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { start(document); });
  } else {
    start(document);
  }
  document.addEventListener('htmx:afterSwap', function (event) {
    start(event.detail && event.detail.elt);
  });
})();

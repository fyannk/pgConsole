/* The wiring diagram a third time: ELK lays it out, this file draws it.
 *
 * Unlike the Cytoscape panel, nothing here renders on a canvas. ELK is
 * asked only where the boxes and the wire corners go, and the answer is
 * turned into the same SVG the server emits — the same classes, the same
 * rounded orthogonal runs, the same row placement — so the drawing
 * inherits the stylesheet, the theme and the print behaviour instead of
 * carrying a second appearance of its own.
 *
 * The served SVG above stays the drawing of record. This panel ships
 * hidden and unhides only once ELK has answered, so a reader without
 * scripting sees the finished drawing and never an empty frame.
 */
(function () {
  'use strict';

  var SVG_NS = 'http://www.w3.org/2000/svg';
  var ARROW_ID = 'topo-arrow-elk';
  var CORNER = 6;

  // The server's own box metrics, so a box drawn here is the size the
  // same box is in the served drawing (wireLineStep / wireBlockHeight /
  // wirePlace in internal/web/wiring.go).
  function lineStep(i) {
    if (i === 0) return 0;
    return i === 1 ? 17 : 14;
  }

  function blockHeight(rows) {
    var h = 14;
    for (var i = 0; i < rows; i++) h += lineStep(i);
    return h;
  }

  function el(name, attrs) {
    var node = document.createElementNS(SVG_NS, name);
    Object.keys(attrs || {}).forEach(function (key) {
      node.setAttribute(key, attrs[key]);
    });
    return node;
  }

  function sign(v) {
    if (v > 0) return 1;
    return v < 0 ? -1 : 0;
  }

  function round(v) {
    return Math.round(v * 100) / 100;
  }

  // The server's roundedRoute: straight runs joined by quadratic corners,
  // with the radius clamped so a short run never overshoots its corner.
  function routePath(points) {
    var d = 'M' + round(points[0].x) + ' ' + round(points[0].y);
    for (var i = 1; i < points.length; i++) {
      var p = points[i];
      if (i === points.length - 1) {
        d += ' L' + round(p.x) + ' ' + round(p.y);
        continue;
      }
      var before = points[i - 1];
      var after = points[i + 1];
      var radius = CORNER;
      var half = (Math.abs(p.x - before.x) + Math.abs(p.y - before.y)) / 2;
      if (half < radius) radius = half;
      half = (Math.abs(after.x - p.x) + Math.abs(after.y - p.y)) / 2;
      if (half < radius) radius = half;
      var inX = sign(p.x - before.x);
      var inY = sign(p.y - before.y);
      var outX = sign(after.x - p.x);
      var outY = sign(after.y - p.y);
      d += ' L' + round(p.x - inX * radius) + ' ' + round(p.y - inY * radius);
      d += ' Q' + round(p.x) + ' ' + round(p.y) +
        ' ' + round(p.x + outX * radius) + ' ' + round(p.y + outY * radius);
    }
    return d;
  }

  // ELK returns each edge as one or more sections of start point, bend
  // points and end point. Flattened, that is the waypoint list the
  // server's router produces.
  function waypoints(edge) {
    var points = [];
    (edge.sections || []).forEach(function (section) {
      points.push(section.startPoint);
      (section.bendPoints || []).forEach(function (bend) { points.push(bend); });
      points.push(section.endPoint);
    });
    return points;
  }

  function toELK(graph) {
    return {
      id: 'root',
      layoutOptions: {
        'elk.algorithm': 'layered',
        // Left to right, like the served drawing.
        'elk.direction': 'RIGHT',
        'elk.edgeRouting': 'ORTHOGONAL',
        'elk.layered.spacing.nodeNodeBetweenLayers': '64',
        'elk.spacing.nodeNode': '22',
        'elk.padding': '[top=12,left=12,bottom=12,right=12]'
      },
      children: (graph.nodes || []).map(function (node) {
        return { id: node.id, width: node.w || 180, height: node.h || 60 };
      }),
      edges: (graph.links || []).map(function (link, index) {
        return {
          id: 'e' + index,
          sources: [link.source],
          targets: [link.target]
        };
      })
    };
  }

  function draw(section, graph, laid) {
    var placed = {};
    (laid.children || []).forEach(function (child) { placed[child.id] = child; });

    var svg = el('svg', {
      class: 'topo',
      viewBox: '0 0 ' + Math.ceil(laid.width) + ' ' + Math.ceil(laid.height),
      role: 'img',
      'aria-label': section.dataset.topoAria || 'Wiring diagram',
      preserveAspectRatio: 'xMidYMid meet'
    });

    var defs = el('defs', {});
    var marker = el('marker', {
      id: ARROW_ID, viewBox: '0 0 10 10', refX: '8.5', refY: '5',
      markerWidth: '7', markerHeight: '7', orient: 'auto-start-reverse'
    });
    marker.appendChild(el('path', { d: 'M0 0 L10 5 L0 10 z' }));
    defs.appendChild(marker);
    svg.appendChild(defs);

    // Flows first, so the boxes sit on top of them as they do server-side.
    (laid.edges || []).forEach(function (edge, index) {
      var points = waypoints(edge);
      if (points.length < 2) return;
      var kind = (graph.links[index] || {}).kind || 'read';
      svg.appendChild(el('path', {
        class: 'topo-edge topo-edge-' + kind,
        d: routePath(points),
        'marker-end': 'url(#' + ARROW_ID + ')'
      }));
    });

    (graph.nodes || []).forEach(function (node) {
      var box = placed[node.id];
      if (!box) return;
      var group = el('g', { class: 'topo-node topo-' + node.cls });
      if (node.state) group.setAttribute('data-state', node.state);
      group.appendChild(el('rect', {
        x: round(box.x), y: round(box.y),
        width: round(box.width), height: round(box.height), rx: 8
      }));
      var rows = node.lines || [];
      var x = box.x + 14;
      var y = box.y + (box.height - blockHeight(rows.length)) / 2 + 11;
      rows.forEach(function (row, i) {
        y += lineStep(i);
        var text = el('text', { class: 'topo-' + row.c, x: round(x), y: round(y) });
        text.textContent = row.t;
        group.appendChild(text);
      });
      svg.appendChild(group);
    });
    return svg;
  }

  function build(section) {
    if (!window.ELK || section.dataset.elkReady === 'true') return;
    var host = section.querySelector('[data-topo-elk-canvas]');
    if (!host) return;
    var graph;
    try {
      graph = JSON.parse(section.dataset.topoGraph);
    } catch (e) {
      return; // The served drawing stays the rendering.
    }
    if (!graph || !graph.nodes || !graph.nodes.length) return;

    section.dataset.elkReady = 'true';
    var elk = new window.ELK();
    elk.layout(toELK(graph)).then(function (laid) {
      // A layout that placed nothing is no diagram: leave the panel
      // hidden rather than unhide an empty frame.
      if (!laid || !laid.children || !laid.children.length) return;
      if (!document.body.contains(section)) return;
      host.replaceChildren(draw(section, graph, laid));
      section.hidden = false;
    }).catch(function () {
      delete section.dataset.elkReady;
    });
  }

  function start(root) {
    var scope = root && root.querySelectorAll ? root : document;
    Array.prototype.forEach.call(scope.querySelectorAll('[data-topo-elk]'), build);
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

/* Force-directed re-layout for the wiring diagram.
 *
 * The diagram is already drawn when this runs: the server computes a
 * geometry and the template renders it, so the Overview shows its wiring
 * with no script at all. This is an enhancement in the strict sense — it
 * replaces a working drawing with a better-spaced one, and its absence
 * costs the reader nothing but even columns.
 *
 * Nodes are pinned to their tier on the x axis (apps -> endpoints ->
 * servers -> storage) and left free on y; a rectangular collision pass
 * keeps boxes and edge labels from overlapping. Edge labels are bodies in
 * the simulation too, so they get pushed out of the way rather than
 * colliding with a box.
 *
 * The relaxation runs to completion synchronously and renders once, so
 * the result is deterministic and safe to screenshot. There is no d3 and
 * no other library: AGENTS.md rule 7 admits no third-party content, and a
 * layered layout of this size does not need one.
 */
(function () {
  'use strict';

  var SVGNS = 'http://www.w3.org/2000/svg';
  var TIER_X = [0, 215, 445, 730];
  var PAD = 16;
  var LABEL_PAD = 8;
  var PASSES = 200;

  function el(name, attrs, parent) {
    var n = document.createElementNS(SVGNS, name);
    for (var k in attrs) {
      if (attrs[k] != null) n.setAttribute(k, attrs[k]);
    }
    if (parent) parent.appendChild(n);
    return n;
  }

  /* Separate overlapping bodies along whichever axis needs the smaller
     push. Labels give way more than boxes do, because a label may sit
     anywhere along its curve while a box may not. */
  function collide(bodies, iterations) {
    for (var it = 0; it < iterations; it++) {
      for (var i = 0; i < bodies.length; i++) {
        for (var j = i + 1; j < bodies.length; j++) {
          var a = bodies[i], b = bodies[j];
          var pad = (a.isLabel || b.isLabel) ? LABEL_PAD : PAD;
          var dx = b.x - a.x, dy = b.y - a.y;
          var ox = (a.w + b.w) / 2 + pad - Math.abs(dx);
          var oy = (a.h + b.h) / 2 + pad - Math.abs(dy);
          if (ox <= 0 || oy <= 0) continue;
          if (oy < ox) {
            var sy = (dy < 0 ? -1 : 1) * oy / 2;
            a.y -= sy;
            b.y += sy;
          } else {
            var sx = (dx < 0 ? -1 : 1) * ox / 2;
            a.x -= sx * (a.isLabel ? 1 : 0.5);
            b.x += sx * (b.isLabel ? 1 : 0.5);
          }
        }
      }
    }
  }

  /* Same-tier links (streaming replication) bow out to the right of the
     column so they never cut across a sibling box; cross-tier links run
     through the gap between tiers. */
  function anchors(s, t) {
    if (s.layer === t.layer) return [s.x + s.w / 2, s.y, t.x + t.w / 2, t.y, true];
    var right = t.x >= s.x;
    return right
      ? [s.x + s.w / 2, s.y, t.x - t.w / 2, t.y, false]
      : [s.x - s.w / 2, s.y, t.x + t.w / 2, t.y, false];
  }

  function path(a) {
    if (a[4]) {
      var bx = Math.max(a[0], a[2]) + 34;
      return 'M' + a[0] + ' ' + a[1] + ' C' + bx + ' ' + a[1] + ' ' + bx + ' ' + a[3] + ' ' + a[2] + ' ' + a[3];
    }
    var mx = (a[0] + a[2]) / 2;
    return 'M' + a[0] + ' ' + a[1] + ' C' + mx + ' ' + a[1] + ' ' + mx + ' ' + a[3] + ' ' + a[2] + ' ' + a[3];
  }

  function build(svg, data) {
    if (!data || !data.nodes || !data.nodes.length) return;

    var nodes = data.nodes.map(function (n, i) {
      return {
        id: n.id, layer: n.layer || 0, cls: n.cls, state: n.state,
        label: n.label, sub: n.sub, disk: n.disk,
        w: n.w || 176,
        h: n.h || (n.disk ? 62 : n.sub ? 52 : 42),
        x: TIER_X[n.layer] || 0,
        y: (i * 37) % 260 - 130
      };
    });
    var byId = {};
    nodes.forEach(function (n) { byId[n.id] = n; });

    var labels = [];
    var links = [];
    (data.links || []).forEach(function (l) {
      var source = byId[l.source], target = byId[l.target];
      if (!source || !target) return;
      var lab = null;
      if (l.label) {
        lab = { isLabel: true, text: l.label, w: l.label.length * 5.4 + 6, h: 13, x: 0, y: 0 };
        labels.push(lab);
      }
      links.push({ source: source, target: target, kind: l.kind, lab: lab });
    });

    var bodies = nodes.concat(labels);

    function place() {
      links.forEach(function (l) {
        if (!l.lab) return;
        var a = anchors(l.source, l.target);
        l.lab.x = a[4] ? Math.max(a[0], a[2]) + 30 : a[0] + (a[2] - a[0]) * 0.66;
        l.lab.y = (a[1] + a[3]) / 2 - 10;
      });
    }

    for (var k = 0; k < PASSES; k++) {
      nodes.forEach(function (n) {
        n.x = TIER_X[n.layer] || 0;
        n.y += (0 - n.y) * 0.02;
      });
      place();
      collide(bodies, 3);
    }
    nodes.forEach(function (n) { n.y = Math.round(n.y); });
    place();
    labels.forEach(function (l) { l.y = Math.round(l.y); });

    /* Replace the served drawing rather than adding to it: the server
       already rendered a complete diagram, and appending would double
       every box. */
    svg.replaceChildren();

    var defs = el('defs', null, svg);
    var marker = el('marker', {
      id: 'topo-arrow', viewBox: '0 0 10 10', refX: 8.5, refY: 5,
      markerWidth: 7, markerHeight: 7, orient: 'auto-start-reverse'
    }, defs);
    el('path', { d: 'M0 0 L10 5 L0 10 z' }, marker);

    links.forEach(function (l) {
      el('path', {
        'class': 'topo-edge topo-edge-' + l.kind,
        d: path(anchors(l.source, l.target)),
        'marker-end': 'url(#topo-arrow)'
      }, svg);
    });
    labels.forEach(function (l) {
      el('text', { 'class': 'topo-elabel', x: Math.round(l.x), y: Math.round(l.y), 'text-anchor': 'middle' }, svg)
        .textContent = l.text;
    });
    nodes.forEach(function (n) {
      var g = el('g', { 'class': 'topo-node topo-' + n.cls, 'data-state': n.state }, svg);
      g.setAttribute('transform', 'translate(' + Math.round(n.x) + ',' + Math.round(n.y) + ')');
      el('rect', { x: -n.w / 2, y: -n.h / 2, width: n.w, height: n.h, rx: 8 }, g);
      var tx = -n.w / 2 + 14;
      var lines = [['topo-label', n.label], ['topo-sub', n.sub], ['topo-disk', n.disk]]
        .filter(function (p) { return p[1]; });
      var ty = -n.h / 2 + (n.h - (lines.length === 3 ? 42 : lines.length === 2 ? 30 : 14)) / 2 + 11;
      lines.forEach(function (p, i) {
        el('text', { 'class': p[0], x: tx, y: ty + i * (i === 2 ? 14 : 17) }, g).textContent = p[1];
      });
    });

    /* Fit the viewBox to the settled drawing so it scales to whatever
       width the container gives it. */
    var x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    bodies.forEach(function (b) {
      x0 = Math.min(x0, b.x - b.w / 2);
      x1 = Math.max(x1, b.x + b.w / 2);
      y0 = Math.min(y0, b.y - b.h / 2);
      y1 = Math.max(y1, b.y + b.h / 2);
    });
    var pad = 16;
    svg.setAttribute('viewBox', [
      Math.round(x0 - pad), Math.round(y0 - pad),
      Math.round(x1 - x0 + pad * 2), Math.round(y1 - y0 + pad * 2)
    ].join(' '));
    svg.setAttribute('data-layout', 'force');
  }

  function start(root) {
	var scope = root && root.querySelectorAll ? root : document;
	var svgs = scope.querySelectorAll('svg.topo:not([data-layout="force"])');
	Array.prototype.forEach.call(svgs, function (svg) {
	  var src = svg.getAttribute('data-topo');
	  if (!src) return;
	  var data;
	  try {
		data = JSON.parse(src);
	  } catch (e) {
		// The served drawing stays exactly as it is.
		return;
	  }
	  build(svg, data);
	});
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

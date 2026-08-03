/* Deterministic re-layout for the wiring diagrams.
 *
 * The diagram is already drawn when this runs: the server computes a
 * geometry and the template renders it, so every page shows its wiring
 * with no script at all. This is an enhancement in the strict sense — it
 * redraws the same graph at the same tiers, which matters only when the
 * container's width budget differs from the served assumption.
 *
 * There is no force pass any more: fixed tiers, one port per arrow, and
 * rounded orthogonal routing through the gaps between tiers, staggered
 * so parallel flows never overlap. Labels live in the server-rendered
 * legend below the drawing; only "replication" stays beside its arrow,
 * because nothing else says what a dashed line between two servers is.
 * The router mirrors the server's (internal/web/toporoute.go); the two
 * must stay in step or the enhanced drawing would not match the served
 * one. There is no d3 and no other library: AGENTS.md rule 7 admits no
 * third-party content.
 */
(function () {
  'use strict';

  var SVGNS = 'http://www.w3.org/2000/svg';
  var TIER_X = [0, 215, 445, 730];
  var TIER_GAP = 22;
  var CORNER = 6;
  var LANE_OFFSET = 20;
  var LANE_GAP = 16;
  var DROP = 14;

  function el(name, attrs, parent) {
    var n = document.createElementNS(SVGNS, name);
    for (var k in attrs) {
      if (attrs[k] != null) n.setAttribute(k, attrs[k]);
    }
    if (parent) parent.appendChild(n);
    return n;
  }

  function sign(v) { return v > 0 ? 1 : v < 0 ? -1 : 0; }

  /* Rounded orthogonal path from axis-aligned waypoints. */
  function route(pts) {
    var d = 'M' + pts[0].x + ' ' + pts[0].y;
    for (var i = 1; i < pts.length; i++) {
      var p = pts[i], prev = pts[i - 1];
      if (i < pts.length - 1) {
        var next = pts[i + 1];
        var dx1 = sign(p.x - prev.x), dy1 = sign(p.y - prev.y);
        var dx2 = sign(next.x - p.x), dy2 = sign(next.y - p.y);
        d += ' L' + (p.x - dx1 * CORNER) + ' ' + (p.y - dy1 * CORNER);
        d += ' Q' + p.x + ' ' + p.y + ' ' + (p.x + dx2 * CORNER) + ' ' + (p.y + dy2 * CORNER);
      } else {
        d += ' L' + p.x + ' ' + p.y;
      }
    }
    return d;
  }

  function build(svg, data) {
    if (!data || !data.nodes || !data.nodes.length) return;

    var tierX = (data.tierX && data.tierX.length) ? data.tierX : TIER_X;

    /* Two node schemas are read: the Overview's label/sub/disk triple and
       the operator view's lines[] list. Both normalise to class + text
       pairs. */
    function rows(n) {
      if (n.lines && n.lines.length) {
        return n.lines.filter(function (l) { return l && l.t; }).map(function (l) {
          return ['topo-' + (l.c === 'label' ? 'label' : l.c === 'sub' ? 'sub' : 'disk'), l.t];
        });
      }
      return [['topo-label', n.label], ['topo-sub', n.sub], ['topo-disk', n.disk]]
        .filter(function (p) { return p[1]; });
    }

    function step(i) { return i === 0 ? 0 : i === 1 ? 17 : 14; }
    function blockHeight(list) {
      var h = 14;
      for (var i = 0; i < list.length; i++) h += step(i);
      return h;
    }

    var nodes = data.nodes.map(function (n) {
      var list = rows(n);
      return {
        id: n.id, layer: n.layer || 0, cls: n.cls, state: n.state,
        rows: list,
        w: n.w || 176,
        h: n.h || 32 + 10 * Math.max(list.length, 1),
        left: tierX[n.layer] || 0,
        top: 0
      };
    });

    /* Fixed tiers: stack each tier's nodes in graph order, centred on a
       shared midline. */
    var tiers = {};
    nodes.forEach(function (n) { (tiers[n.layer] = tiers[n.layer] || []).push(n); });
    var tallest = 0;
    Object.keys(tiers).forEach(function (layer) {
      var stack = tiers[layer];
      var height = stack.reduce(function (sum, n) { return sum + n.h; }, 0) + (stack.length - 1) * TIER_GAP;
      if (height > tallest) tallest = height;
    });
    var mid = tallest / 2 + 16;
    Object.keys(tiers).forEach(function (layer) {
      var stack = tiers[layer];
      var height = stack.reduce(function (sum, n) { return sum + n.h; }, 0) + (stack.length - 1) * TIER_GAP;
      var y = mid - height / 2;
      stack.forEach(function (n) {
        n.top = y;
        y += n.h + TIER_GAP;
      });
    });

    var byId = {};
    nodes.forEach(function (n) { byId[n.id] = n; });

    /* The router, mirroring internal/web/toporoute.go. */
    var flows = [];
    (data.links || []).forEach(function (l) {
      var src = byId[l.source], dst = byId[l.target];
      if (!src || !dst) return;
      flows.push({ kind: l.kind, src: src, dst: dst, sameTier: src.layer === dst.layer, direct: false, lane: 0 });
    });

    var bySource = {};
    flows.forEach(function (f) {
      if (f.sameTier) (bySource[f.src.id] = bySource[f.src.id] || []).push(f);
    });
    Object.keys(bySource).forEach(function (id) {
      var group = bySource[id].slice().sort(function (a, b) { return a.dst.top - b.dst.top; });
      var lane = 0;
      group.forEach(function (f, i) {
        if (i === 0 && f.dst.top > f.src.top) { f.direct = true; return; }
        f.lane = lane++;
      });
    });

    var sides = {};
    function addSide(id, side, f) {
      var key = id + '|' + side;
      (sides[key] = sides[key] || []).push(f);
    }
    flows.forEach(function (f) {
      if (f.sameTier && f.direct) { addSide(f.src.id, 'bottom', f); addSide(f.dst.id, 'top', f); return; }
      if (f.sameTier) { addSide(f.src.id, 'bottom', f); addSide(f.dst.id, 'rightin', f); return; }
      addSide(f.src.id, 'right', f); addSide(f.dst.id, 'left', f);
    });
    Object.keys(sides).forEach(function (key) {
      var parts = key.split('|');
      var node = byId[parts[0]];
      var side = parts[1];
      var out = side === 'right' || side === 'bottom';
      var group = sides[key].slice().sort(function (a, b) {
        var ca = out ? a.dst : a.src, cb = out ? b.dst : b.src;
        var ya = ca.top + ca.h / 2, yb = cb.top + cb.h / 2;
        return ya !== yb ? ya - yb : ca.left - cb.left;
      });
      group.forEach(function (f, i) {
        var slot = (i + 1) / (group.length + 1);
        if (side === 'right') { f.sx = node.left + node.w; f.sy = node.top + node.h * slot; }
        if (side === 'left') { f.tx = node.left; f.ty = node.top + node.h * slot; }
        if (side === 'bottom') { f.sx = node.left + node.w * slot; f.sy = node.top + node.h; }
        if (side === 'top') { f.tx = node.left + node.w * slot; f.ty = node.top; }
        if (side === 'rightin') { f.tx = node.left + node.w; f.ty = node.top + node.h * slot; }
      });
    });

    var byGap = {};
    flows.forEach(function (f) {
      if (!f.sameTier) (byGap[f.src.layer] = byGap[f.src.layer] || []).push(f);
    });
    Object.keys(byGap).forEach(function (layer) {
      var group = byGap[layer].slice().sort(function (a, b) { return a.ty - b.ty; });
      var left = -Infinity, right = Infinity;
      group.forEach(function (f) {
        if (f.sx > left) left = f.sx;
        if (f.tx < right) right = f.tx;
      });
      group.forEach(function (f, i) {
        f.gapX = left + (right - left) * (i + 1) / (group.length + 1);
      });
    });

    var columnRight = {};
    nodes.forEach(function (n) {
      var edge = n.left + n.w;
      if (!(n.layer in columnRight) || edge > columnRight[n.layer]) columnRight[n.layer] = edge;
    });

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

    var labeled = false;
    flows.forEach(function (f) {
      var pts;
      if (f.sameTier && f.direct) {
        pts = [{ x: f.sx, y: f.sy }, { x: f.sx, y: f.ty }];
      } else if (f.sameTier) {
        var lane = columnRight[f.src.layer] + LANE_OFFSET + f.lane * LANE_GAP;
        var turn = f.sy + DROP;
        pts = [{ x: f.sx, y: f.sy }, { x: f.sx, y: turn }, { x: lane, y: turn }, { x: lane, y: f.ty }, { x: f.tx, y: f.ty }];
      } else if (f.sy === f.ty) {
        pts = [{ x: f.sx, y: f.sy }, { x: f.tx, y: f.ty }];
      } else {
        pts = [{ x: f.sx, y: f.sy }, { x: f.gapX, y: f.sy }, { x: f.gapX, y: f.ty }, { x: f.tx, y: f.ty }];
      }
      el('path', {
        'class': 'topo-edge topo-edge-' + f.kind,
        d: route(pts),
        'marker-end': 'url(#topo-arrow)'
      }, svg);
      if (f.kind === 'replicate' && !labeled) {
        labeled = true;
        el('text', {
          'class': 'topo-elabel',
          x: Math.round(pts[0].x) + 8,
          y: Math.round((pts[0].y + pts[pts.length - 1].y) / 2) + 4
        }, svg).textContent = 'replication';
      }
    });

    nodes.forEach(function (n) {
      var g = el('g', { 'class': 'topo-node topo-' + n.cls, 'data-state': n.state }, svg);
      g.setAttribute('transform', 'translate(' + Math.round(n.left + n.w / 2) + ',' + Math.round(n.top + n.h / 2) + ')');
      el('rect', { x: -n.w / 2, y: -n.h / 2, width: n.w, height: n.h, rx: 8 }, g);
      var tx = -n.w / 2 + 14;
      var y = -n.h / 2 + (n.h - blockHeight(n.rows)) / 2 + 11;
      n.rows.forEach(function (p, i) {
        y += step(i);
        el('text', { 'class': p[0], x: tx, y: y }, g).textContent = p[1];
      });
    });

    /* Fit the viewBox to the drawing. getBBox measures the real
       geometry — lanes included — and is synchronous, so the layout
       stays deterministic; the pad also covers marker overhang, which
       getBBox does not include. */
    var x0 = 0, y0 = 0, x1 = 920, y1 = 260;
    var box = null;
    try { box = svg.getBBox(); } catch (e) { box = null; }
    if (box && box.width > 0 && box.height > 0) {
      x0 = box.x; y0 = box.y; x1 = box.x + box.width; y1 = box.y + box.height;
    }
    var pad = 16;
    svg.setAttribute('viewBox', [
      Math.round(x0 - pad), Math.round(y0 - pad),
      Math.round(x1 - x0 + pad * 2), Math.round(y1 - y0 + pad * 2)
    ].join(' '));
    /* Hold the drawing at its natural size — one user unit per pixel — so
       the type stays at the size it was designed at. Past that the CSS cap
       still limits growth; below it the scroll shell takes over. */
    var natural = Math.min(Math.round(x1 - x0 + pad * 2), 1040);
    svg.style.minWidth = natural + 'px';
    svg.setAttribute('data-layout', 'routed');
  }

  function start(root) {
    var scope = root && root.querySelectorAll ? root : document;
    var svgs = scope.querySelectorAll('svg.topo:not([data-layout="routed"])');
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

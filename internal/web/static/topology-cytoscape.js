/* The wiring diagram again, drawn by Cytoscape.js.
 *
 * The SVG above it is the complete rendering: settled server-side, with
 * every box and wire already placed. This panel adds nothing to the
 * record — it reads the same graph the server put in the document and
 * hands it to a layout engine that runs in the browser, so the diagram
 * can be panned, zoomed and pulled apart. It stays hidden until it has
 * actually drawn something, so a reader without scripting sees the
 * finished drawing and no empty frame.
 *
 * Cytoscape is given the graph, not a picture of it: the tiers are the
 * console's, the routing and the placement are Cytoscape's.
 */
(function () {
  'use strict';

  var instances = [];

  // The design system states colour in oklch, which Cytoscape's own
  // parser rejects — every such value is dropped and the box falls back
  // to a default, silently losing the health border. Painting the value
  // into a one-pixel canvas and reading the pixel back converts it to
  // the sRGB triple Cytoscape does understand, without hard-coding a
  // second copy of the palette here.
  var probe = null;

  function toRGB(value) {
    if (!value) return value;
    if (!probe) {
      var canvas = document.createElement('canvas');
      canvas.width = canvas.height = 1;
      probe = canvas.getContext('2d', { willReadFrequently: true });
    }
    if (!probe) return value;
    try {
      probe.clearRect(0, 0, 1, 1);
      probe.fillStyle = value;
      probe.fillRect(0, 0, 1, 1);
      var px = probe.getImageData(0, 0, 1, 1).data;
      return 'rgb(' + px[0] + ',' + px[1] + ',' + px[2] + ')';
    } catch (e) {
      return value;
    }
  }

  function cssVar(name) {
    return toRGB(getComputedStyle(document.documentElement).getPropertyValue(name).trim());
  }

  // Box fill and border, mirroring the .topo-* rules in the stylesheet.
  // Health is a border colour reinforcing a word the box already says.
  function boxStyle(node) {
    var fill = cssVar('--surface');
    var line = cssVar('--border-strong');
    var width = 1.5;
    if (node.cls === 'endpoint') {
      fill = cssVar('--surface-2');
      line = cssVar('--border');
    } else if (node.cls === 'pooler' || node.cls === 'snapshot') {
      fill = cssVar('--surface-2');
    } else if (node.cls === 'storage') {
      fill = cssVar('--accent-weak');
      line = cssVar('--accent');
    }
    if (node.state === 'current') {
      line = cssVar('--ok');
    } else if (node.state === 'degraded') {
      fill = cssVar('--bad-weak');
      line = cssVar('--bad');
    }
    if (node.cls === 'primary') width = 3;
    return { fill: fill, line: line, width: width };
  }

  function label(node) {
    return (node.lines || []).map(function (row) { return row.t; }).join('\n');
  }

  var EDGE = {
    write: { color: '--accent', dash: [] },
    read: { color: '--text-muted', dash: [] },
    replicate: { color: '--ok', dash: [5, 4] },
    archive: { color: '--accent', dash: [2, 4] }
  };

  function elements(graph) {
    var out = [];
    (graph.nodes || []).forEach(function (node) {
      var style = boxStyle(node);
      out.push({
        group: 'nodes',
        data: {
          id: node.id,
          text: label(node),
          layer: node.layer,
          w: node.w || 180,
          h: node.h || 60,
          fill: style.fill,
          line: style.line,
          lineWidth: style.width
        }
      });
    });
    (graph.links || []).forEach(function (link, index) {
      var kind = EDGE[link.kind] || EDGE.read;
      out.push({
        group: 'edges',
        data: {
          id: 'e' + index,
          source: link.source,
          target: link.target,
          color: cssVar(kind.color),
          dash: kind.dash
        }
      });
    });
    return out;
  }

  // Fit the whole graph, but never magnify past its natural size: at
  // zoom 1 the boxes are exactly as wide as the served drawing's, and
  // blowing them up would only make the panel look like a different
  // diagram. A graph too big to fit legibly is pannable instead.
  function settle(cy) {
    cy.fit(undefined, 16);
    if (cy.zoom() > 1) {
      cy.zoom(1);
      cy.center();
    }
  }

  var CYTO_STYLESHEET_ID = '__________cytoscape_stylesheet';

  function claimCytoscapeStylesheetID() {
    if (document.getElementById(CYTO_STYLESHEET_ID)) return;
    // A <meta>, not a <style>: Cytoscape only looks the id up, and an
    // empty <style> element is itself an inline style the policy
    // refuses. This element carries nothing and styles nothing.
    var placeholder = document.createElement('meta');
    placeholder.id = CYTO_STYLESHEET_ID;
    document.head.appendChild(placeholder);
  }

  function build(section) {
    var host = section.querySelector('[data-topo-cyto-canvas]');
    if (!window.cytoscape || !host || section.dataset.cytoReady === 'true') return;
    var graph;
    try {
      graph = JSON.parse(section.dataset.topoGraph);
    } catch (e) {
      return; // The SVG above stays the rendering.
    }
    if (!graph || !graph.nodes || !graph.nodes.length) return;

    // Cytoscape wants to inject a <style> element carrying the
    // container's `position: relative`, which the served
    // Content-Security-Policy blocks — the rule is in the stylesheet
    // instead. It skips the injection when an element already owns that
    // id, so claiming the id with an empty one keeps a refused inline
    // style out of every page load. Empty, so there is nothing to block.
    claimCytoscapeStylesheetID();

    // Cytoscape measures its container, so the panel has to be visible
    // before it draws. If anything below throws, it goes back to hidden.
    section.hidden = false;
    var cy;
    try {
      cy = window.cytoscape({
        container: host,
        elements: elements(graph),
        style: [
          {
            selector: 'node',
            style: {
              shape: 'round-rectangle',
              width: 'data(w)',
              height: 'data(h)',
              'background-color': 'data(fill)',
              'border-color': 'data(line)',
              'border-width': 'data(lineWidth)',
              label: 'data(text)',
              color: cssVar('--text'),
              'font-family': getComputedStyle(document.documentElement)
                .getPropertyValue('--font-ui').trim() || 'sans-serif',
              'font-size': 10,
              'text-wrap': 'wrap',
              'text-valign': 'center',
              'text-halign': 'center',
              'text-max-width': 'data(w)'
            }
          },
          {
            selector: 'edge',
            style: {
              // Taxi edges are the orthogonal runs the served drawing
              // uses, so the two diagrams read the same way.
              'curve-style': 'taxi',
              'taxi-direction': 'vertical',
              'line-color': 'data(color)',
              'line-dash-pattern': 'data(dash)',
              'line-style': 'dashed',
              width: 1.5,
              'target-arrow-color': 'data(color)',
              'target-arrow-shape': 'triangle',
              'arrow-scale': 0.8
            }
          },
          {
            selector: 'edge[dash.length = 0]',
            style: { 'line-style': 'solid' }
          }
        ],
        layout: {
          // Cytoscape's own hierarchical layout, taken as it comes. It
          // runs top to bottom where the served drawing runs left to
          // right, and that is left alone on purpose: breadthfirst
          // separates tiers by node height and siblings by node width,
          // so rotating it a quarter turn swaps both and collapses the
          // tiers into each other. The tiers are the console's; the
          // placement is entirely Cytoscape's.
          name: 'breadthfirst',
          directed: true,
          padding: 20,
          // Sibling separation is the widest node times this factor, so
          // anything under about 0.87 makes these boxes touch. Just above
          // it leaves a visible gap and still fits the tree in the panel
          // at zoom 1, where the type is the size it is in the drawing.
          spacingFactor: 0.95,
          roots: graph.nodes.filter(function (n) { return n.layer === 0; })
            .map(function (n) { return n.id; })
        },
        minZoom: 0.2,
        maxZoom: 3
      });
    } catch (e) {
      section.hidden = true;
      return;
    }
    settle(cy);
    instances.push({ cy: cy, section: section });
    section.dataset.cytoReady = 'true';
  }

  function drop(entry) {
    entry.cy.destroy();
    delete entry.section.dataset.cytoReady;
  }

  // Every colour above was sampled from the theme, so a theme swap means
  // drawing it again.
  function rebuild() {
    instances.forEach(drop);
    instances = [];
    Array.prototype.forEach.call(
      document.querySelectorAll('[data-topo-cyto]'),
      function (section) {
        var host = section.querySelector('[data-topo-cyto-canvas]');
        if (host) host.replaceChildren();
        build(section);
      }
    );
  }

  function start(root) {
    var scope = root && root.querySelectorAll ? root : document;
    instances = instances.filter(function (entry) {
      if (document.body.contains(entry.section)) return true;
      entry.cy.destroy();
      return false;
    });
    Array.prototype.forEach.call(scope.querySelectorAll('[data-topo-cyto]'), build);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { start(document); });
  } else {
    start(document);
  }
  document.addEventListener('htmx:afterSwap', function (event) {
    start(event.detail && event.detail.elt);
  });
  new MutationObserver(rebuild).observe(document.documentElement, {
    attributes: true, attributeFilter: ['data-theme']
  });
  window.addEventListener('resize', function () {
    instances.forEach(function (entry) {
      if (!document.body.contains(entry.section)) return;
      entry.cy.resize();
      settle(entry.cy);
    });
  });
})();

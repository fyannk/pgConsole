/* Progressive enhancement for the console. Every page is complete and
   usable before this file runs: the server renders the full document,
   and nothing here fetches, mutates, or interprets cluster state. These
   components only re-arrange and hide markup the server already sent.

   Loaded before alpine.csp.js so the alpine:init registrations below
   exist by the time Alpine starts. Both tags are deferred, which keeps
   that order.

   The CSP build of Alpine parses a restricted expression grammar, so
   behaviour lives in registered component methods rather than inline
   directive expressions, and DOM wiring that would need arguments in an
   expression is attached here with addEventListener instead. */

document.addEventListener('alpine:init', () => {
  Alpine.data('panel', panel);
  Alpine.data('dataTable', dataTable);
  Alpine.data('autoRefresh', autoRefresh);
  Alpine.data('sidebar', sidebar);
});

/**
 * Reads a persisted preference, tolerating storage being unavailable
 * (private mode, disabled cookies, sandboxed frame).
 * @param {string} key Storage key.
 * @param {string|null} fallback Value to use when unreadable or unset.
 * @returns {string|null} The stored value, or the fallback.
 */
function readPref(key, fallback) {
  try {
    const v = window.localStorage.getItem(key);
    return v === null ? fallback : v;
  } catch (e) {
    return fallback;
  }
}

/**
 * Writes a preference, ignoring storage failures.
 * @param {string} key Storage key.
 * @param {string} value Value to persist.
 * @returns {void}
 */
function writePref(key, value) {
  try {
    window.localStorage.setItem(key, value);
  } catch (e) {
    /* Preference persistence is optional; the page works without it. */
  }
}

/**
 * Collapsible panel. The body is visible by default and stays visible
 * when this never runs, so collapsing is additive. The open/closed
 * choice persists per panel name taken from data-panel.
 * @returns {object} Alpine component.
 */
function panel() {
  return {
    open: true,
    root: null,
    storageKey: '',

    /**
     * Restores the persisted state for this panel and wires the toggle.
     * @returns {void}
     */
    init() {
      this.root = this.$el;
      const button = this.root.querySelector('.panel-toggle');
      if (button) button.addEventListener('click', () => this.toggle());
      const name = this.root.dataset.panel || '';
      if (name) {
        this.storageKey = 'pgconsole.panel.' + name;
        this.open = readPref(this.storageKey, 'open') !== 'closed';
      }
      this.apply();
    },

    /**
     * Reflects the open state onto the DOM. Written imperatively for the
     * same reason the click is: the markup then carries no colon-prefixed
     * attribute name, which a strict XML serialiser would read as a
     * namespace prefix and reject.
     * @returns {void}
     */
    apply() {
      if (!this.root) return;
      const body = this.root.querySelector('.panel-body');
      if (body) body.hidden = !this.open;
      const button = this.root.querySelector('.panel-toggle');
      if (button) button.textContent = this.label;
    },

    /**
     * Toggles the body and persists the new state.
     * @returns {void}
     */
    toggle() {
      this.open = !this.open;
      if (this.storageKey) {
        writePref(this.storageKey, this.open ? 'open' : 'closed');
      }
      this.apply();
    },

    /** @returns {string} Button label for the current state. */
    get label() {
      return this.open ? 'Hide' : 'Show';
    },
  };
}

/**
 * Collapsible left navigation. The served markup is the expanded state,
 * so the nav is complete without this; collapsing only narrows it to
 * its icons. The choice persists across page loads.
 * @returns {object} Alpine component.
 */
function sidebar() {
  return {
    collapsed: false,
    root: null,

    /**
     * Restores the persisted width choice and wires the toggle.
     * @returns {void}
     */
    init() {
      this.root = this.$el;
      const button = this.root.querySelector('.sidebar-toggle');
      if (button) button.addEventListener('click', () => this.toggle());
      this.collapsed = readPref('pgconsole.sidebar', 'expanded') === 'collapsed';
      this.apply();
    },

    /**
     * Reflects the current state onto the DOM, imperatively for the same
     * reason as panel above.
     * @returns {void}
     */
    apply() {
      const el = this.root;
      if (!el) return;
      el.dataset.collapsed = this.collapsed ? 'true' : 'false';
      const button = el.querySelector('.sidebar-toggle');
      if (!button) return;
      button.setAttribute('aria-label', this.toggleLabel);
      button.textContent = this.chevron;
    },

    /**
     * Switches between the labelled and icon-only widths.
     * @returns {void}
     */
    toggle() {
      this.collapsed = !this.collapsed;
      writePref('pgconsole.sidebar', this.collapsed ? 'collapsed' : 'expanded');
      this.apply();
    },

    /** @returns {string} Accessible label for the toggle. */
    get toggleLabel() {
      return this.collapsed ? 'Expand navigation' : 'Collapse navigation';
    },

    /** @returns {string} Chevron pointing the way the nav will move. */
    get chevron() {
      return this.collapsed ? '›' : '‹';
    },
  };
}

/**
 * Sort key for one cell. Values the server renders as "unknown" sort
 * after everything else in both directions, so an absent fact never
 * masquerades as a low or high value.
 * @param {HTMLTableCellElement} cell Cell to read.
 * @returns {{unknown: boolean, num: number|null, text: string}} Key.
 */
function sortKey(cell) {
  const text = (cell.textContent || '').trim();
  const lower = text.toLowerCase();
  const unknown = lower === '' || lower === 'unknown' || lower.startsWith('unknown');
  const m = text.match(/^-?\d+(\.\d+)?/);
  return { unknown: unknown, num: m ? parseFloat(m[0]) : null, text: lower };
}

/**
 * Compares two cells for sorting.
 * @param {HTMLTableCellElement} a Left cell.
 * @param {HTMLTableCellElement} b Right cell.
 * @returns {number} Negative, zero or positive ordering result.
 */
function compareCells(a, b) {
  const ka = sortKey(a);
  const kb = sortKey(b);
  if (ka.unknown !== kb.unknown) return ka.unknown ? 1 : -1;
  if (ka.num !== null && kb.num !== null && ka.num !== kb.num) return ka.num - kb.num;
  return ka.text.localeCompare(kb.text, undefined, { numeric: true });
}

/**
 * Client-side filter and sort over one server-rendered table. Rows are
 * never fetched or created; matching only sets display on existing
 * rows, and sorting only reorders them.
 * @returns {object} Alpine component.
 */
function dataTable() {
  return {
    query: '',
    total: 0,
    visible: 0,
    rows: [],
    body: null,
    headers: [],
    sortCol: -1,
    sortAsc: true,

    /**
     * Captures the rendered rows and makes the header cells sortable.
     * @returns {void}
     */
    init() {
      const table = this.$el.querySelector('table');
      if (!table || !table.tBodies.length) return;
      this.body = table.tBodies[0];
      this.rows = Array.prototype.slice.call(this.body.rows);
      this.total = this.rows.length;
      this.visible = this.total;

      if (table.tHead && table.tHead.rows.length) {
        this.headers = Array.prototype.slice.call(table.tHead.rows[0].cells);
        this.headers.forEach((th, index) => {
          th.dataset.sortable = '';
          th.tabIndex = 0;
          th.setAttribute('role', 'button');
          th.addEventListener('click', () => this.sort(index));
          th.addEventListener('keydown', (event) => {
            if (event.key !== 'Enter' && event.key !== ' ') return;
            event.preventDefault();
            this.sort(index);
          });
        });
      }

      this.$watch('query', () => this.apply());
    },

    /**
     * Hides rows that do not contain the query as a substring.
     * @returns {void}
     */
    apply() {
      const needle = this.query.trim().toLowerCase();
      let shown = 0;
      this.rows.forEach((row) => {
        const hit = needle === '' ||
          (row.textContent || '').toLowerCase().indexOf(needle) !== -1;
        row.style.display = hit ? '' : 'none';
        if (hit) shown += 1;
      });
      this.visible = shown;
    },

    /**
     * Sorts by a column, toggling direction when it is already active.
     * @param {number} index Zero-based column index.
     * @returns {void}
     */
    sort(index) {
      this.sortAsc = this.sortCol === index ? !this.sortAsc : true;
      this.sortCol = index;

      const direction = this.sortAsc ? 1 : -1;
      const ordered = this.rows.slice().sort((ra, rb) => {
        const ca = ra.cells[index];
        const cb = rb.cells[index];
        if (!ca || !cb) return 0;
        return compareCells(ca, cb) * direction;
      });
      ordered.forEach((row) => this.body.appendChild(row));

      this.headers.forEach((th, i) => {
        if (i === index) th.dataset.sort = this.sortAsc ? 'asc' : 'desc';
        else delete th.dataset.sort;
      });
    },

    /** @returns {string} Row count summary for the tools bar. */
    get summary() {
      if (this.visible === this.total) return this.total + ' rows';
      return this.visible + ' of ' + this.total + ' rows';
    },
  };
}

/**
 * Optional periodic full-page reload. This deliberately reloads rather
 * than patching the DOM: the page is a server-rendered snapshot, and a
 * reload is the only refresh that cannot show a mix of two snapshots or
 * invent state the server did not send. Off by default.
 * @returns {object} Alpine component.
 */
function autoRefresh() {
  return {
    on: false,
    period: 30,
    remaining: 30,
    timer: null,

    /**
     * Restores the persisted choice and starts the timer if enabled.
     * @returns {void}
     */
    init() {
      this.on = readPref('pgconsole.autorefresh', 'off') === 'on';
      this.$watch('on', (value) => {
        writePref('pgconsole.autorefresh', value ? 'on' : 'off');
        this.restart();
      });
      this.restart();
    },

    /**
     * Clears any running timer and starts a fresh one when enabled.
     * @returns {void}
     */
    restart() {
      if (this.timer !== null) {
        window.clearInterval(this.timer);
        this.timer = null;
      }
      this.remaining = this.period;
      if (!this.on) return;
      this.timer = window.setInterval(() => this.tick(), 1000);
    },

    /**
     * Counts down one second, reloading at zero.
     * @returns {void}
     */
    tick() {
      this.remaining -= 1;
      if (this.remaining > 0) return;
      window.clearInterval(this.timer);
      this.timer = null;
      window.location.reload();
    },

    /**
     * Stops the timer when the component leaves the DOM.
     * @returns {void}
     */
    destroy() {
      if (this.timer !== null) window.clearInterval(this.timer);
    },

    /** @returns {string} Human-readable timer state. */
    get status() {
      return this.on ? 'reloading in ' + this.remaining + 's' : 'paused';
    },
  };
}

/* Progressive enhancement for the console. Every page is complete and
   usable before this file runs: the server renders the full document.
   Alpine components own local controls; refresh delegates a same-origin
   GET and whole-root swap to htmx. Nothing here interprets cluster state.

   Loaded before the Alpine CSP build so the alpine:init registrations
   below exist by the time Alpine starts. Both tags are deferred, which
   keeps that order.

   The CSP build of Alpine parses a restricted expression grammar, so
   behaviour lives in registered component methods rather than inline
   directive expressions, and DOM wiring that would need arguments in an
   expression is attached here with addEventListener instead. */

document.addEventListener('alpine:init', () => {
  Alpine.data('dataTable', dataTable);
  Alpine.data('autoRefresh', autoRefresh);
  Alpine.data('sidebar', sidebar);
});

/**
 * Reports an enhanced-request failure without replacing the last complete
 * server-rendered screen. The message is constant: response bodies and
 * transport errors are never reflected into the document.
 * @returns {void}
 */
function reportRefreshFailure() {
  const target = document.querySelector('.refresh-error');
  if (target) target.textContent = 'Refresh failed; the previous snapshot remains visible.';
}

document.addEventListener('htmx:responseError', reportRefreshFailure);
document.addEventListener('htmx:sendError', reportRefreshFailure);

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

/* ---------------------------------------------------------------- Theme

   Two themes only: Navy Chrome (light) and Midnight Console (dark).
   data-theme on <html> pins one; with no attribute the OS decides.
   Applied as early as this file runs so the pinned theme wins the first
   paint, and the click is delegated so an htmx swap keeps working. */

function currentThemeIsDark() {
  const root = document.documentElement;
  if (root.dataset.theme === 'dark') return true;
  if (root.dataset.theme === 'light') return false;
  return window.matchMedia('(prefers-color-scheme: dark)').matches;
}

const storedTheme = readPref('pgconsole.theme', '');
if (storedTheme === 'light' || storedTheme === 'dark') {
  document.documentElement.dataset.theme = storedTheme;
}

document.addEventListener('click', (event) => {
  const button = event.target.closest && event.target.closest('.theme-toggle');
  if (!button) return;
  const next = currentThemeIsDark() ? 'light' : 'dark';
  document.documentElement.dataset.theme = next;
  writePref('pgconsole.theme', next);
});

/* ------------------------------------------------------------- Tabs, rows

   Delegated so an htmx swap keeps them working, and additive: without
   this every tab panel stays visible and the pod name link still works. */

function selectTab(button) {
  const list = button.closest('.tabs');
  if (!list) return;
  const buttons = Array.prototype.slice.call(list.querySelectorAll('button[data-tab]'));
  buttons.forEach((b) => {
    const on = b === button;
    b.setAttribute('aria-selected', on ? 'true' : 'false');
    const panel = document.getElementById(b.dataset.tab);
    if (panel) panel.hidden = !on;
  });
}

document.addEventListener('click', (event) => {
  const target = event.target;
  if (!target.closest) return;

  const tab = target.closest('.tabs button[data-tab]');
  if (tab) { selectTab(tab); return; }

  const open = target.closest('[data-dialog]');
  if (open) {
    const dialog = document.getElementById(open.dataset.dialog);
    if (dialog && dialog.showModal) dialog.showModal();
    return;
  }

  const close = target.closest('[data-dialog-close]');
  if (close) {
    const dialog = close.closest('dialog');
    if (dialog) dialog.close();
    return;
  }

  /* A click on the backdrop lands on the dialog element itself, never on
     its content, so it closes. */
  if (target.tagName === 'DIALOG' && target.open) { target.close(); return; }

  /* A click anywhere on the row follows the row's own link, unless the
     click already landed on one. */
  const row = target.closest('tr[data-href]');
  if (row && !target.closest('a')) window.location.href = row.dataset.href;
});

/* ------------------------------------------------------------ Log follow

   The served tail is complete on its own. Following re-fetches the same
   bounded tail from the pod's raw route and keeps the pane pinned to its
   last line; a scroll away from the bottom stops it, as tail -f does not
   fight the reader. The route is the same one the page rendered from, so
   following can never show a line the reader's level would not get. */

function initLogTails(scope) {
  const root = scope && scope.querySelectorAll ? scope : document;
  Array.prototype.forEach.call(root.querySelectorAll('pre[data-log-tail]'), (pre) => {
    if (pre.dataset.logReady === 'true') return;
    pre.dataset.logReady = 'true';
    const body = pre.closest('.panel-body') || document;
    const box = body.querySelector('[data-log-follow]');
    const state = body.querySelector('[data-log-state]');
    const src = pre.dataset.logSrc;

    const reflect = () => {
      const on = !box || box.checked;
      if (state) {
        state.textContent = on ? 'following' : 'paused';
        if (on) state.removeAttribute('data-paused');
        else state.setAttribute('data-paused', '');
      }
      if (on) pre.scrollTop = pre.scrollHeight;
    };

    /* Fit the pane to the space left below it so only the pane scrolls. */
    const fit = () => {
      /* A hidden panel has no geometry to measure. */
      if (!pre.offsetParent) return;
      const gap = 16;
      const footer = pre.parentElement
        ? pre.parentElement.getBoundingClientRect().bottom - pre.getBoundingClientRect().bottom
        : 0;
      const height = window.innerHeight - pre.getBoundingClientRect().top - footer - gap;
      pre.style.height = Math.max(96, Math.floor(height)) + 'px';
      /* Whatever sits below the pane (caption, source line, padding) is
         measured as page overflow and taken back out of the pane, so the
         document itself never scrolls. */
      const spill = document.documentElement.scrollHeight - window.innerHeight;
      if (spill > 0) pre.style.height = Math.max(96, pre.clientHeight - spill) + 'px';
      if (!box || box.checked) pre.scrollTop = pre.scrollHeight;
    };
    fit();
    window.addEventListener('resize', fit);
    document.addEventListener('click', (event) => {
      if (event.target.closest && event.target.closest('.tabs button[data-tab]')) window.setTimeout(fit, 0);
    });

    if (box) box.addEventListener('change', reflect);

    /* Scrolling away pauses; scrolling back to the bottom resumes. */
    pre.addEventListener('scroll', () => {
      if (!box) return;
      const atBottom = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 24;
      if (box.checked !== atBottom) { box.checked = atBottom; reflect(); }
    });

    if (src) {
      window.setInterval(() => {
        if ((box && !box.checked) || document.hidden || !document.body.contains(pre)) return;
        fetch(src, { credentials: 'same-origin' }).then((resp) => {
          if (!resp.ok) throw new Error('status');
          return resp.text();
        }).then((text) => {
          if (box && !box.checked) return;
          pre.textContent = text;
          pre.scrollTop = pre.scrollHeight;
        }).catch(() => {
          /* The last fetched tail stays; the page's own refresh cycle
             reports transport failures. */
        });
      }, 5000);
    }

    reflect();
  });
}

function initTabs(scope) {
  const root = scope && scope.querySelectorAll ? scope : document;
  Array.prototype.forEach.call(root.querySelectorAll('.tabs'), (list) => {
    const first = list.querySelector('button[data-tab][aria-selected="true"]') ||
      list.querySelector('button[data-tab]');
    if (first) selectTab(first);
  });
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', () => { initTabs(document); initLogTails(document); });
} else {
  initTabs(document);
  initLogTails(document);
}
document.addEventListener('htmx:afterSwap', (event) => {
  const scope = event.detail && event.detail.elt;
  initTabs(scope);
  initLogTails(scope);
});


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
     * Restores the persisted width choice.
     * @returns {void}
     */
    init() {
      /* Captured here: $el inside a handler method is the element the
         directive sits on (the button), not the component root. */
      this.root = this.$el;
      const button = this.root.querySelector('.sidebar-toggle');
      if (button) button.addEventListener('click', () => this.toggle());
      this.collapsed = readPref('pgconsole.sidebar', 'expanded') === 'collapsed';
      this.apply();
    },

    /**
     * Reflects the current state onto the DOM. Written imperatively
     * rather than through a colon-prefixed x-bind so the markup stays
     * free of namespace-like attribute names.
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
 * Optional periodic application-root refresh. The response is still one
 * complete server-rendered Page; htmx selects and replaces the root in one
 * swap so the browser never assembles individual cards from different
 * responses. Off by default.
 * @returns {object} Alpine component.
 */
function autoRefresh() {
  return {
    on: false,
    period: 30,
    remaining: 30,
    timer: null,
	button: null,

    /**
     * Restores the persisted choice and starts the timer if enabled.
     * @returns {void}
     */
    init() {
	  this.button = this.$el.querySelector('.refresh-now');
	  if (this.button) {
	    this.button.addEventListener('click', () => this.refresh());
	  }
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
	  this.remaining = this.period;
	  this.refresh();
    },

	/**
	 * Requests the current route through the declarative htmx trigger. The
	 * response replaces the one application root; hx-sync on the document
	 * makes a newer navigation or refresh supersede this request.
	 * @returns {void}
	 */
	refresh() {
	  if (!this.button || !window.htmx) return;
	  const error = this.$el.querySelector('.refresh-error');
	  if (error) error.textContent = '';
	  window.htmx.trigger(this.button, 'pgconsole:refresh');
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

// Browser-side checks for the console UI.
//
// Driven by hack/test-ui.sh against the fixture harness in
// internal/web/uiharness_test.go. Everything asserted here is a property
// of the served page in a real engine, which is exactly what the Go
// tests cannot reach: whether the enhancement layer runs at all under
// the served Content-Security-Policy, whether colour contrast clears
// WCAG AA in both schemes, and whether a table survives a narrow
// viewport.
//
// Exits non-zero on the first failing check. Screenshots and a summary
// land in artifacts/ui/ for CI to upload.

'use strict';

const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const BASE = parseInt(process.env.PGCONSOLE_UI_PORT_BASE || '18090', 10);
const HOST = process.env.PGCONSOLE_UI_HOST || '127.0.0.1';
const OUT = process.env.PGCONSOLE_UI_ARTIFACTS ||
  path.join(__dirname, '..', '..', 'artifacts', 'ui');

const STATES = {
  healthy: `http://${HOST}:${BASE}/`,
  stale: `http://${HOST}:${BASE + 1}/`,
  degraded: `http://${HOST}:${BASE + 2}/`,
  empty: `http://${HOST}:${BASE + 3}/`,
};

const results = [];

/**
 * Records one check and prints it.
 * @param {string} name What was checked.
 * @param {boolean} pass Whether it held.
 * @param {string} [detail] Evidence, printed either way.
 * @returns {void}
 */
function check(name, pass, detail) {
  results.push({ name, pass, detail: detail || '' });
  console.log(`${pass ? 'PASS' : 'FAIL'}  ${name}${detail ? '  — ' + detail : ''}`);
}

/**
 * Collects console errors, page errors and >=400 responses for a page.
 * @param {import('playwright').Page} page Page to watch.
 * @returns {string[]} Live array, appended to as the page runs.
 */
function watch(page) {
  const errors = [];
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));
  page.on('response', (r) => {
    if (r.status() >= 400) errors.push(`HTTP ${r.status()} ${r.url()}`);
  });
  return errors;
}

/** @returns {string} axe-core's browser bundle source. */
function axeSource() {
  return fs.readFileSync(require.resolve('axe-core/axe.min.js'), 'utf8');
}

/**
 * Runs axe against the current page, restricted to WCAG 2 A and AA.
 * @param {import('playwright').Page} page Page to audit.
 * @returns {Promise<object[]>} Violations, most severe first.
 */
async function audit(page) {
  await page.evaluate(axeSource());
  return page.evaluate(async () => {
    const r = await window.axe.run(document, {
      runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa'] },
    });
    return r.violations.map((v) => ({
      id: v.id,
      impact: v.impact,
      nodes: v.nodes.length,
      worst: (v.nodes[0] && v.nodes[0].failureSummary || '')
        .split('\n').filter(Boolean).slice(-1)[0] || '',
    }));
  });
}

/**
 * Verifies the enhancement layer loads and every interaction works.
 * @param {import('playwright').Browser} browser Browser to use.
 * @returns {Promise<void>}
 */
async function checkEnhancement(browser) {
  // The proxy always asserts identity and level in production; this
  // context mirrors that, and it is what clears the revision-detail
  // gate the history flow below exercises.
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 1200 },
    extraHTTPHeaders: { 'X-Forwarded-User': 'operator', 'X-PgToolBox-Level': 'poweruser' },
  });
  const page = await ctx.newPage();
  const errors = watch(page);
  await page.goto(STATES.healthy, { waitUntil: 'networkidle' });

  const csp = errors.filter((e) => /Content Security Policy|Refused to/i.test(e));
  check('no CSP violations', csp.length === 0, csp.join(' | '));

  const version = await page.evaluate(() => (window.Alpine || {}).version || '');
  check('Alpine started under script-src self', version !== '', `version ${version || 'absent'}`);
  const htmxVersion = await page.evaluate(() => (window.htmx || {}).version || '');
  check('htmx started under script-src self', htmxVersion === '2.0.10',
    `version ${htmxVersion || 'absent'}`);

  const others = errors.filter((e) => !/Content Security Policy|Refused to/i.test(e));
  check('no console errors or failed requests', others.length === 0, others.slice(0, 4).join(' | '));

  // The wiring diagram lives on the Overview, so it is checked before
  // moving on to the screens that carry the tables.
  const topo = await page.evaluate(() => {
    const svg = document.querySelector('svg.topo');
    if (!svg) return { layout: 'missing', boxes: 0, dup: 0 };
    const boxes = svg.querySelectorAll('.topo-node').length;
    const ids = new Set([...svg.querySelectorAll('.topo-node .topo-label')].map((n) => n.textContent));
    return { layout: svg.getAttribute('data-layout'), boxes, dup: boxes - ids.size };
  });
  check('wiring diagram is re-laid out when the enhancement runs',
    topo.layout === 'force' && topo.boxes > 0, `layout ${topo.layout}, ${topo.boxes} boxes`);
  // Appending instead of replacing would double every box.
  check('re-layout replaces the served drawing rather than adding to it',
    topo.dup === 0, `${topo.dup} duplicated boxes`);

  // The pod roster is its own screen since the shell rebuild.
  await page.goto(new URL('/cluster/pods', STATES.healthy).toString(), { waitUntil: 'networkidle' });

  const toolsVisible = await page.locator('.table-tools').first().isVisible();
  check('enhancement controls revealed after init', toolsVisible);

  // Filter.
  const search = page.locator('section[data-panel="pods"] .table-tools input[type="search"]');
  const rows = page.locator('section[data-panel="pods"] table.pods tbody tr');
  const total = await rows.count();
  await search.fill('orders-2');
  await page.waitForTimeout(250);
  const shown = await rows.evaluateAll(
    (rs) => rs.filter((r) => getComputedStyle(r).display !== 'none').length);
  check('table filter narrows rows', total === 3 && shown === 1, `${total} rows -> ${shown} visible`);

  const summary = await page.locator('section[data-panel="pods"] .table-tools .count').innerText();
  check('filter reports the count', /1 of 3 rows/.test(summary), summary);
  await search.fill('');
  await page.waitForTimeout(250);

  // Sort.
  const nameHeader = page.locator('section[data-panel="pods"] table.pods thead th').first();
  await nameHeader.click();
  await page.waitForTimeout(150);
  const asc = await rows.first().locator('td').first().innerText();
  await nameHeader.click();
  await page.waitForTimeout(150);
  const desc = await rows.first().locator('td').first().innerText();
  check('click-to-sort reorders rows', asc === 'orders-1' && desc === 'orders-3',
    `asc=${asc}, desc=${desc}`);
  check('sorted header marked for the stylesheet',
    (await nameHeader.getAttribute('data-sort')) === 'desc');

  // Tabs, on the pod detail: with no script every panel is served
  // visible; the enhancement shows one at a time and the click moves it.
  await page.goto(new URL('/cluster/pods/orders-1', STATES.healthy).toString(), { waitUntil: 'networkidle' });
  const statusPanel = page.locator('#pod-status');
  const historyPanel = page.locator('#pod-history');
  const statusFirst = await statusPanel.isVisible() && !(await historyPanel.isVisible());
  await page.locator('.tabs button[data-tab="pod-history"]').click();
  await page.waitForTimeout(150);
  const historyAfter = (await historyPanel.isVisible()) && !(await statusPanel.isVisible());
  check('pod detail tabs switch panels', statusFirst && historyAfter,
    `status-first=${statusFirst}, history-after=${historyAfter}`);
  await page.goto(new URL('/cluster/events', STATES.healthy).toString(), { waitUntil: 'networkidle' });

  // Auto-refresh must never be on unless asked for.
  check('auto-refresh defaults to off',
    (await page.locator('.refresh input[type="checkbox"]').isChecked()) === false);

  // Sidebar: collapse narrows it to the icon rail and back.
  const aside = page.locator('aside.sidebar');
  const widthOf = () => aside.evaluate((el) => Math.round(el.getBoundingClientRect().width));
  const expanded = await widthOf();
  await page.locator('.sidebar-toggle').click();
  await page.waitForTimeout(150);
  const collapsed = await widthOf();
  check('sidebar collapses to the icon rail', expanded > 200 && collapsed < 80,
    `${expanded}px -> ${collapsed}px`);
  check('collapsed sidebar hides its labels',
    (await page.locator('.sidebar-link[aria-current="page"] .sidebar-label').isVisible()) === false);
  await page.locator('.sidebar-toggle').click();
  await page.waitForTimeout(150);
  check('sidebar expands again', (await widthOf()) === expanded);

  // Enhanced navigation replaces one application root while preserving
  // the document, then the manual refresh uses the same atomic path.
  await page.evaluate(() => { window.__pgconsoleDocumentMarker = 'survives-root-swaps'; });
  const navigationCount = await page.evaluate(() => performance.getEntriesByType('navigation').length);
  await page.locator('a.sidebar-link[title="Object history"]').click();
  await page.waitForURL(/\/history$/);
  await page.waitForTimeout(200);
  const enhanced = await page.evaluate(() => ({
    marker: window.__pgconsoleDocumentMarker,
    navigations: performance.getEntriesByType('navigation').length,
    roots: document.querySelectorAll('#pgconsole-app').length,
  }));
  check('sidebar navigation swaps one application root without reloading the document',
    enhanced.marker === 'survives-root-swaps' && enhanced.navigations === navigationCount && enhanced.roots === 1,
    `${enhanced.navigations} navigation entries, ${enhanced.roots} app roots`);
  check('history timeline enhancement draws from the served rows',
    await page.locator('[data-history-visual] svg .history-point').count() === 3);

  const refreshed = page.evaluate(() => new Promise((resolve) => {
    document.addEventListener('htmx:afterSwap', () => resolve(true), { once: true });
  }));
  await page.locator('.refresh-now').click();
  await refreshed;
  check('manual refresh preserves the browser document',
    await page.evaluate(() => window.__pgconsoleDocumentMarker === 'survives-root-swaps'));

  const cachedHistory = await page.evaluate(() => Object.keys(localStorage)
    .filter((key) => key.toLowerCase().includes('htmx'))
    .map((key) => [key, localStorage.getItem(key)]));
  check('htmx stores no page history in localStorage', cachedHistory.length === 0,
    JSON.stringify(cachedHistory));

  await page.locator('a[href^="/history/revisions/"]').first().click();
  await page.waitForURL(/\/history\/revisions\/\d+$/);
  check('revision detail loads into the live history screen',
    (await page.locator('#history-detail').innerText()).includes('Structural diff'));
  check('refresh follows the selected revision URL',
    (await page.locator('.refresh-now').getAttribute('hx-get')) === new URL(page.url()).pathname);

  // A destination this build does not serve is present but inert: shown
  // so the map stays complete, never a link that would 404.
  const disabled = page.locator('.sidebar-link[aria-disabled="true"]');
  const disabledCount = await disabled.count();
  const anyIsAnchor = await disabled.evaluateAll(
    (els) => els.some((e) => e.tagName.toLowerCase() === 'a' || e.hasAttribute('href')));
  check('unbuilt destinations are shown but not links',
    disabledCount > 0 && anyIsAnchor === false,
    `${disabledCount} disabled entries, any anchor: ${anyIsAnchor}`);

  // Every live sidebar link must resolve — a 404 in the nav is a lie
  // about what this build serves.
  const hrefs = await page.locator('.sidebar-link[href^="/"]').evaluateAll(
    (els) => els.map((e) => e.getAttribute('href')));
  const broken = [];
  for (const href of hrefs) {
    const res = await page.request.get(new URL(href, STATES.healthy).toString());
    if (res.status() >= 400) broken.push(`${href} -> ${res.status()}`);
  }
  check('every live sidebar link resolves', broken.length === 0,
    broken.join(', ') || `${hrefs.length} links checked`);

  await ctx.close();
}

/**
 * Verifies the page is complete and honest with scripting disabled.
 * @param {import('playwright').Browser} browser Browser to use.
 * @returns {Promise<void>}
 */
async function checkNoScript(browser) {
  const ctx = await browser.newContext({
    javaScriptEnabled: false, viewport: { width: 1440, height: 1400 },
  });
  const page = await ctx.newPage();
  await page.goto(STATES.healthy, { waitUntil: 'domcontentloaded' });

  // The wiring diagram is served drawn. The force re-layout is an
  // enhancement, so with script off the boxes and flows must still be
  // there — an empty <svg> waiting to be filled would mean the Overview
  // silently loses its diagram for anyone without JavaScript.
  const topoNoJs = await page.evaluate(() => {
    const svg = document.querySelector('svg.topo');
    if (!svg) return { boxes: -1, edges: -1, layout: 'missing' };
    return {
      boxes: svg.querySelectorAll('.topo-node').length,
      edges: svg.querySelectorAll('.topo-edge').length,
      layout: svg.getAttribute('data-layout') || 'server',
    };
  });
  check('wiring diagram is drawn without JavaScript',
    topoNoJs.boxes > 0 && topoNoJs.edges > 0 && topoNoJs.layout === 'server',
    `${topoNoJs.boxes} boxes, ${topoNoJs.edges} flows, layout ${topoNoJs.layout}`);

  await page.goto(new URL('/cluster/pods', STATES.healthy).toString(), { waitUntil: 'domcontentloaded' });
  check('panel bodies visible without JavaScript',
    await page.locator('section[data-panel="pods"] .panel-body').isVisible());
  const rows = await page.locator('section[data-panel="pods"] table.pods tbody tr').count();
  check('table rows present without JavaScript', rows === 3, `${rows} rows`);
  check('enhancement controls hidden without JavaScript',
    (await page.locator('.table-tools').first().isVisible()) === false);
  check('auto-refresh hidden without JavaScript',
    (await page.locator('.refresh').isVisible()) === false);

  const state = await page.locator('dl.target dd[data-state]').innerText();
  check('state word present without JavaScript', /current/.test(state), state);


  // The topbar snapshot must carry its state hue as well as its word.
  // `dl.target dd` outranks the bare [data-state] tokens on specificity,
  // so the colour is restated for the topbar; when that restatement is
  // missing the mark still renders and only the hue silently flattens to
  // the body text colour, which no rendered-string test can see.
  const hue = await page.evaluate(() => {
    const dd = document.querySelector('dl.target dd[data-state]');
    const body = document.querySelector('body');
    return {
      state: getComputedStyle(dd).color,
      text: getComputedStyle(body).color,
    };
  });
  check('topbar snapshot carries its state hue, not the body text colour',
    hue.state !== hue.text, `state ${hue.state} vs text ${hue.text}`);

  await page.screenshot({ path: path.join(OUT, 'healthy-nojs-1440.png'), fullPage: true });
  await ctx.close();
}

/**
 * Audits every state in both colour schemes and captures screenshots.
 * @param {import('playwright').Browser} browser Browser to use.
 * @returns {Promise<void>}
 */
async function checkStates(browser) {
  for (const scheme of ['light', 'dark']) {
    for (const [name, url] of Object.entries(STATES)) {
      const ctx = await browser.newContext({
        colorScheme: scheme, viewport: { width: 1440, height: 1400 },
      });
      const page = await ctx.newPage();
      const errors = watch(page);
      await page.goto(url, { waitUntil: 'networkidle' });
      await page.waitForTimeout(300);

      const violations = await audit(page);
      const serious = violations.filter(
        (v) => v.impact === 'critical' || v.impact === 'serious');
      check(`${name}/${scheme}: no serious accessibility violations`, serious.length === 0,
        serious.map((v) => `${v.id}(${v.nodes}) ${v.worst}`).join(' | ') ||
          (violations.length ? violations.map((v) => `${v.id}(${v.impact})`).join(',') : 'clean'));
      check(`${name}/${scheme}: no console errors or failed requests`, errors.length === 0,
        errors.slice(0, 3).join(' | '));

      await page.screenshot({
        path: path.join(OUT, `${name}-${scheme}-1440.png`), fullPage: true,
      });
      await ctx.close();
    }
  }
}

/**
 * Verifies narrow viewports neither overflow the body nor shred words
 * inside table cells.
 * @param {import('playwright').Browser} browser Browser to use.
 * @returns {Promise<void>}
 */
async function checkResponsive(browser) {
  for (const width of [375, 768, 1024, 1440]) {
    const ctx = await browser.newContext({ viewport: { width, height: 1200 } });
    const page = await ctx.newPage();
    await page.goto(STATES.healthy, { waitUntil: 'networkidle' });
    await page.waitForTimeout(200);

    const overflow = await page.evaluate(() =>
      document.documentElement.scrollWidth > document.documentElement.clientWidth + 1);
    check(`no horizontal page overflow at ${width}px`, !overflow);

    // The table checks below need a screen that carries one; the
    // Overview stopped being that screen with the shell rebuild.
    await page.goto(new URL('/cluster/pods', STATES.healthy).toString(), { waitUntil: 'networkidle' });
    await page.waitForTimeout(200);
    const podsOverflow = await page.evaluate(() =>
      document.documentElement.scrollWidth > document.documentElement.clientWidth + 1);
    check(`no horizontal page overflow on the pod roster at ${width}px`, !podsOverflow);

    // Guards the specific regression that shredded every table value
    // into single characters at narrow widths. `overflow-wrap: anywhere`
    // drops a cell's min-content width to one character, so table
    // auto-layout collapses each column to nothing; `break-word` keeps
    // the longest word as the floor. Asserting the computed value is
    // exact, where measuring rendered text is not: a long image
    // reference wrapping at 375px is correct behaviour, not a defect.
    const wrap = await page.evaluate(() => {
      const cell = document.querySelector('table.pods tbody td');
      return cell ? getComputedStyle(cell).overflowWrap : 'no-cell';
    });
    check(`table cells do not collapse min-content at ${width}px`, wrap === 'break-word',
      `overflow-wrap: ${wrap}`);

    // With the column floor honoured the table keeps its width and the
    // surrounding .table-scroll takes the overflow, rather than the
    // columns compressing until values break apart.
    const kept = await page.evaluate(() => {
      const table = document.querySelector('table.pods');
      const scroll = table && table.closest('.table-scroll');
      return table && scroll
        ? { table: Math.round(table.getBoundingClientRect().width), scrollable: scroll.scrollWidth > scroll.clientWidth }
        : null;
    });
    check(`table keeps its column floor at ${width}px`,
      kept !== null && kept.table >= 600,
      kept ? `table ${kept.table}px, scroll container ${kept.scrollable ? 'scrolls' : 'fits'}` : 'table missing');

    if (width === 375) {
      await page.screenshot({ path: path.join(OUT, 'healthy-light-375.png'), fullPage: true });
    }
    await ctx.close();
  }
}

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const launch = {};
  if (process.env.PLAYWRIGHT_CHROMIUM_PATH) {
    launch.executablePath = process.env.PLAYWRIGHT_CHROMIUM_PATH;
  }
  const browser = await chromium.launch(launch);
  try {
    await checkEnhancement(browser);
    await checkNoScript(browser);
    await checkStates(browser);
    await checkResponsive(browser);
  } finally {
    await browser.close();
  }

  const failed = results.filter((r) => !r.pass);
  const summary = `${results.length - failed.length}/${results.length} checks passed`;
  fs.writeFileSync(path.join(OUT, 'summary.txt'),
    results.map((r) => `${r.pass ? 'PASS' : 'FAIL'}  ${r.name}${r.detail ? '  — ' + r.detail : ''}`)
      .join('\n') + `\n\n${summary}\n`);
  console.log(`\n${summary}`);
  console.log(`artifacts in ${OUT}`);
  process.exit(failed.length === 0 ? 0 : 1);
})().catch((e) => {
  console.error('driver error:', e && e.stack || e);
  process.exit(2);
});

import assert from 'node:assert/strict';
import { chromium } from 'playwright';

const launchURL = process.env.BB_LAUNCH_URL;
if (!launchURL) {
  console.error('BB_LAUNCH_URL is required');
  process.exit(2);
}

const parsed = new URL(launchURL);
const baseURL = `${parsed.protocol}//${parsed.host}`;

const browser = await chromium.launch({ headless: true });

try {
  const context = await browser.newContext();
  const page = await context.newPage();

  const consoleErrors = [];
  const pageErrors = [];

  page.on('console', (msg) => {
    if (msg.type() === 'error') {
      consoleErrors.push(msg.text());
    }
  });
  page.on('pageerror', (err) => {
    pageErrors.push(err.message);
  });

  // 1. Unauthenticated access must return 401 with the launch-link prompt.
  const unauthResp = await page.goto(`${baseURL}/dashboard`, { waitUntil: 'domcontentloaded' });
  assert.equal(unauthResp?.status(), 401, 'unauthenticated /dashboard should return 401');
  const unauthBody = await page.textContent('body');
  assert.ok(unauthBody?.includes('Open the link that bb serve printed'), 'missing launch-link prompt');

  // 2. Opening the printed launch URL authenticates via cookie and lands on /.
  // Clear console errors caused by the intentional unauthenticated 401 check above.
  consoleErrors.length = 0;
  pageErrors.length = 0;
  const authResp = await page.goto(launchURL, { waitUntil: 'networkidle' });
  assert.equal(authResp?.status(), 200, 'launch URL should redirect to 200 landing page');
  assert.equal(new URL(page.url()).pathname, '/', 'launch URL should redirect to /');
  assert.equal(new URL(page.url()).search, '', 'launch token must not stay in address bar');

  // 3. Navigate core routes and assert clean rendering.
  for (const path of ['/dashboard', '/architecture', '/starters', '/docs', '/docs/getting-started']) {
    const resp = await page.goto(`${baseURL}${path}`, { waitUntil: 'networkidle' });
    assert.equal(resp?.status(), 200, `GET ${path} should return 200`);
  }

  // 4. Verify experiment services appear on the dashboard.
  await page.goto(`${baseURL}/dashboard`, { waitUntil: 'networkidle' });
  const dashText = await page.textContent('body');
  assert.ok(dashText?.includes('postgres'), 'dashboard should list postgres service');
  assert.ok(dashText?.includes('redis'), 'dashboard should list redis service');

  // 5. Verify /search-index returns JSON results.
  const searchData = await page.evaluate(async () => {
    const res = await fetch('/search-index');
    return { status: res.status, items: await res.json() };
  });
  assert.equal(searchData.status, 200, '/search-index should return 200');
  assert.ok(Array.isArray(searchData.items) && searchData.items.length > 0, '/search-index should be non-empty');

  assert.deepEqual(consoleErrors, [], `unexpected browser console errors: ${consoleErrors.join('; ')}`);
  assert.deepEqual(pageErrors, [], `unexpected uncaught page errors: ${pageErrors.join('; ')}`);

  console.log('e2e smoke: ok (auth, landing, dashboard, architecture, starters, docs, search-index, zero console/page errors)');
} finally {
  await browser.close();
}

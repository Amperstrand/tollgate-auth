import { test, expect } from '@playwright/test';
import { mintTestToken } from './fixtures';

test.describe('OCPI eMSP Dashboard', () => {

  test.beforeEach(async ({ request }) => {
    await request.post('/api/charger/stop');
  });

  test('dashboard loads with virtual charger panel', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('h1')).toContainText('tollgate-auth · OCPI 2.2.1 eMSP');
    await expect(page.locator('text=Virtual Charger')).toBeVisible();
    await expect(page.locator('.charger-box')).toBeVisible();
  });

  test('charger shows AVAILABLE state with Plug In button', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.charger-box')).toHaveClass(/AVAILABLE/);
    await expect(page.locator('#charge-cashu')).toBeVisible();
    await expect(page.locator('button:has-text("Plug In")')).toBeVisible();
  });

  test('OCPI protocol endpoints are correct', async ({ request }) => {
    const versions = await request.get('/ocpi/versions');
    expect(versions.ok()).toBeTruthy();
    const vBody = await versions.json();
    expect(vBody.data.versions[0].version).toBe('2.2.1');

    const details = await request.get('/ocpi/emsp/2.2.1/version_details');
    expect(details.ok()).toBeTruthy();
    const dBody = await details.json();
    const modules = dBody.data.endpoints.map((e: any) => e.identifier);
    expect(modules).toContain('credentials');
    expect(modules).toContain('tokens');
    expect(modules).toContain('sessions');
    expect(modules).toContain('cdrs');
    expect(modules).toContain('locations');
  });

  test('charger status API returns valid state', async ({ request }) => {
    const resp = await request.get('/api/charger/status');
    expect(resp.ok()).toBeTruthy();
    const body = await resp.json();
    expect(body.status_code).toBe(1000);
    expect(['AVAILABLE', 'CHARGING', 'BLOCKED']).toContain(body.data.state);
  });

  test('full charge cycle: Cashu → CHARGING → kWh → Stop → CDR', async ({ page, request }) => {
    test.setTimeout(120_000);

    const cashuToken = await mintTestToken(5);
    expect(cashuToken).toMatch(/^cashuB/);

    await request.post('/api/charger/stop');

    const startResp = await request.post('/api/charger/start', {
      data: { cashu_token: cashuToken }
    });
    const startBody = await startResp.json();
    expect(startBody.status_code).toBe(1000);
    expect(startBody.data.state).toBe('CHARGING');
    expect(startBody.data.session.credit_amount).toBe(5);

    await page.goto('/');
    await expect(page.locator('.charger-box')).toHaveClass(/CHARGING/);
    await expect(page.locator('#live-kwh')).toBeVisible();

    const kwh1 = parseFloat(await page.locator('#live-kwh').textContent() || '0');
    await page.waitForTimeout(3000);
    await page.reload();
    const kwh2 = parseFloat(await page.locator('#live-kwh').textContent() || '0');
    expect(kwh2).toBeGreaterThan(kwh1);

    const stopResp = await request.post('/api/charger/stop');
    const stopBody = await stopResp.json();
    expect(stopBody.data.state).toBe('AVAILABLE');
    expect(stopBody.data.kwh).toBeGreaterThan(0);

    const snap = await request.get('/api/snapshot');
    const snapBody = await snap.json();
    const cdrs = snapBody.data.CDRs || [];
    expect(cdrs.length).toBeGreaterThan(0);
    const lastCdr = cdrs[0];
    expect(lastCdr.total_kwh).toBeGreaterThan(0);
    expect(lastCdr.currency).toBe('NOK');
    expect(lastCdr.total_cost).toBeGreaterThan(0);
  });

  test('invalid token keeps charger AVAILABLE for retry', async ({ page }) => {
    await page.goto('/');

    await expect(page.locator('.charger-box')).toHaveClass(/AVAILABLE/);
    await page.locator('#charge-cashu').fill('cashuBfake_invalid_token');
    await page.locator('button:has-text("Plug In")').click();

    await page.waitForTimeout(3000);

    await expect(page.locator('.charger-box')).toHaveClass(/AVAILABLE/);
    await expect(page.locator('#charge-cashu')).toBeVisible();
  });

  test('health endpoint is ok', async ({ request }) => {
    const resp = await request.get('/healthz');
    expect(resp.ok()).toBeTruthy();
    const body = await resp.json();
    expect(body.status).toBe('ok');
  });
});

import { test, expect, type Page } from '@playwright/test';

const logText = Array.from({ length: 300 }, (_, i) => `[INFO]   : line ${i + 1} of the oc-mirror output`).join('\n');

async function openLogs(page: Page) {
  const now = new Date().toISOString();
  await page.route('**/api/operations', route => route.fulfill({
    json: [{ id: 'logs1', name: 'logged run', configFile: 'isc.yaml', status: 'success', startedAt: now, completedAt: now, logs: [] }],
  }));
  await page.route('**/api/operations/logs1/logs', route => route.fulfill({ json: { logs: logText } }));
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto('/operations');
  await page.getByRole('button', { name: 'Actions for logged run' }).click();
  await page.getByRole('menuitem', { name: 'View Logs' }).click();
  const card = page.locator('#operation-logs-card');
  await expect(card.getByText('line 300 of the oc-mirror output')).toBeAttached();
  return card;
}

async function logHeight(page: Page) {
  return page.locator('#log-container').evaluate(el => el.getBoundingClientRect().height);
}

async function isScrolledToBottom(page: Page) {
  return page.locator('#log-container').evaluate(el => el.scrollHeight - el.scrollTop - el.clientHeight < 2);
}

test.describe('Operation logs console', () => {
  test('expands to fill the window and collapses back', async ({ page }) => {
    const card = await openLogs(page);
    expect(await logHeight(page)).toBeLessThanOrEqual(400);
    await expect(card.getByRole('button', { name: 'Back to Top' })).toBeVisible();

    await card.getByRole('button', { name: 'Expand' }).click();
    await expect(card.getByRole('button', { name: 'Collapse' })).toHaveAttribute('aria-pressed', 'true');
    await expect(card.getByRole('button', { name: 'Back to Top' })).toBeHidden();
    const box = await card.boundingBox();
    expect(box!.height).toBeGreaterThan(800);
    expect(box!.width).toBeGreaterThan(1200);
    expect(await logHeight(page)).toBeGreaterThan(600);
    await expect.poll(() => isScrolledToBottom(page)).toBe(true);
    expect(await page.evaluate(() => document.body.style.overflow)).toBe('hidden');

    await card.getByRole('button', { name: 'Collapse' }).click();
    await expect(card.getByRole('button', { name: 'Expand' })).toHaveAttribute('aria-pressed', 'false');
    expect(await logHeight(page)).toBeLessThanOrEqual(400);
    expect(await page.evaluate(() => document.body.style.overflow)).toBe('');
  });

  test('Escape collapses the expanded console', async ({ page }) => {
    const card = await openLogs(page);
    await card.getByRole('button', { name: 'Expand' }).click();
    expect(await logHeight(page)).toBeGreaterThan(600);
    await page.keyboard.press('Escape');
    await expect(card.getByRole('button', { name: 'Expand' })).toBeVisible();
    expect(await logHeight(page)).toBeLessThanOrEqual(400);
  });

  test('dragging the handle resizes the console and the height is remembered', async ({ page }) => {
    const card = await openLogs(page);
    const handle = card.getByRole('separator', { name: 'Resize operation logs' });
    await expect(handle).toHaveAttribute('aria-valuenow', '400');

    await handle.scrollIntoViewIfNeeded();
    const box = (await handle.boundingBox())!;
    const x = box.x + box.width / 2;
    const y = box.y + box.height / 2;
    await page.mouse.move(x, y);
    await page.mouse.down();
    await page.mouse.move(x, y + 150, { steps: 5 });
    await page.mouse.up();
    expect(await logHeight(page)).toBeCloseTo(550, -1);
    await expect(handle).toHaveAttribute('aria-valuenow', '550');
    await expect.poll(() => isScrolledToBottom(page)).toBe(true);

    // Drag well past the minimum: the console stops at 120px.
    await handle.scrollIntoViewIfNeeded();
    const box2 = (await handle.boundingBox())!;
    await page.mouse.move(x, box2.y + box2.height / 2);
    await page.mouse.down();
    await page.mouse.move(x, box2.y - 1000, { steps: 5 });
    await page.mouse.up();
    expect(await logHeight(page)).toBeCloseTo(120, -1);

    await page.keyboard.press('ArrowDown');
    await expect(handle).toHaveAttribute('aria-valuenow', '160');

    await page.reload();
    await page.getByRole('button', { name: 'Actions for logged run' }).click();
    await page.getByRole('menuitem', { name: 'View Logs' }).click();
    await expect(page.getByRole('separator', { name: 'Resize operation logs' })).toHaveAttribute('aria-valuenow', '160');
    expect(await logHeight(page)).toBeCloseTo(160, -1);
  });

  test('keyboard resizes the console and double-click resets it', async ({ page }) => {
    const card = await openLogs(page);
    const handle = card.getByRole('separator', { name: 'Resize operation logs' });
    await handle.focus();
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('ArrowDown');
    await expect(handle).toHaveAttribute('aria-valuenow', '480');
    await page.keyboard.press('ArrowUp');
    await expect(handle).toHaveAttribute('aria-valuenow', '440');
    await page.keyboard.press('Home');
    await expect(handle).toHaveAttribute('aria-valuenow', '120');
    await page.keyboard.press('End');
    const max = Number(await handle.getAttribute('aria-valuemax'));
    await expect(handle).toHaveAttribute('aria-valuenow', String(max));
    expect(await logHeight(page)).toBeCloseTo(max, -1);

    await handle.dblclick();
    await expect(handle).toHaveAttribute('aria-valuenow', '400');
    expect(await page.evaluate(() => localStorage.getItem('mirror-gui-logs-height'))).toBeNull();
  });

  test('the resize handle is hidden while the console is expanded', async ({ page }) => {
    const card = await openLogs(page);
    await card.getByRole('button', { name: 'Expand' }).click();
    await expect(card.getByRole('separator', { name: 'Resize operation logs' })).toHaveCount(0);
    await card.getByRole('button', { name: 'Collapse' }).click();
    await expect(card.getByRole('separator', { name: 'Resize operation logs' })).toBeVisible();
  });
});

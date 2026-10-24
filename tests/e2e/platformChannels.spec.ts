import { test, expect, type Page } from '@playwright/test';

async function addChannel(page: Page) {
  await page.goto('/config');
  await expect(page.getByRole('heading', { name: 'Platform Channels' })).toBeVisible({ timeout: 15_000 });
  await page.getByRole('button', { name: 'Add platform channel' }).click();
  await expect(page.getByText('Channel: stable-4.16')).toBeVisible();
}

async function chooseType(page: Page, type: string) {
  await page.getByRole('button', { name: 'Channel type for channel 1' }).click();
  await page.getByRole('option', { name: new RegExp(`^${type}\\b`) }).click();
}

async function previewYaml(page: Page): Promise<string> {
  await page.getByRole('tab', { name: /Preview/ }).click();
  await expect(page.getByText('ImageSetConfiguration Preview')).toBeVisible({ timeout: 10_000 });
  return (await page.locator('#yaml-preview').textContent()) ?? '';
}

test.describe('Platform channels', () => {
  test('channel type can be stable, fast, candidate or eus', async ({ page }) => {
    await addChannel(page);
    for (const type of ['fast', 'candidate', 'eus', 'stable']) {
      await chooseType(page, type);
      await expect(page.getByText(`Channel: ${type}-4.16`)).toBeVisible();
    }
  });

  test('warns that EUS is only published for even 4.x releases', async ({ page }) => {
    await addChannel(page);
    await chooseType(page, 'eus');
    await page.getByPlaceholder('Release, e.g. 4.22').fill('4.21');
    await page.getByRole('option', { name: '4.21', exact: true }).click();
    await expect(page.getByText(/eus-4\.21 does not exist/)).toBeVisible();
  });

  test('accepts a future release and writes it to the YAML', async ({ page }) => {
    await addChannel(page);
    await chooseType(page, 'candidate');
    await page.getByPlaceholder('Release, e.g. 4.22').fill('5.0');
    await page.getByRole('option', { name: 'Use release "5.0"' }).click();
    await expect(page.getByText('Channel: candidate-5.0')).toBeVisible();

    const yaml = await previewYaml(page);
    expect(yaml).toMatch(/name:\s*candidate-5\.0/);
    expect(yaml).toMatch(/type:\s*ocp/);
  });

  test('rejects a release that is not major.minor', async ({ page }) => {
    await addChannel(page);
    await page.getByPlaceholder('Release, e.g. 4.22').fill('five');
    await page.getByRole('option', { name: 'Use release "five"' }).click();
    await expect(page.getByText(/"five" is not a release version/)).toBeVisible();
    await expect(page.getByText('Channel: stable-4.16')).toBeVisible();
  });
});

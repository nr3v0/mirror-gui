import { test, expect, type Page } from '@playwright/test';

const images = [
  'quay.io/openshift-release-dev/ocp-release:4.22.1-x86_64',
  'registry.redhat.io/redhat/certified-operator-index:v4.22',
  'registry.redhat.io/redhat/redhat-operator-index:v4.22',
];

async function openImageSources(page: Page) {
  const now = new Date().toISOString();
  await page.route('**/api/operations', route => route.fulfill({
    json: [{ id: 'preview1', name: 'image preview', configFile: 'isc.yaml', status: 'success', dryRun: true, startedAt: now, completedAt: now, logs: [] }],
  }));
  await page.route('**/api/operations/preview1/images*', route => route.fulfill({
    json: {
      operationId: 'preview1',
      imageCount: images.length,
      images,
      registries: [{ prefix: 'quay.io/openshift-release-dev', count: 1 }, { prefix: 'registry.redhat.io/redhat', count: 2 }],
    },
  }));
  await page.goto('/operations');
  await page.getByRole('button', { name: 'Actions for image preview' }).click();
  await page.getByText('View Image Sources').click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByText('Image Sources', { exact: true })).toBeVisible();
  return dialog;
}

async function clipboard(page: Page): Promise<string> {
  return page.evaluate(() => navigator.clipboard.readText());
}

test.describe('Image Sources dialog', () => {
  test.beforeEach(async ({ context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  });

  test('registries tab lists and copies host plus first path segment', async ({ page }) => {
    const dialog = await openImageSources(page);
    await expect(dialog.getByRole('tab', { name: 'Registries (2)' })).toHaveAttribute('aria-selected', 'true');
    await expect(dialog.getByRole('gridcell', { name: 'registry.redhat.io/redhat' })).toBeVisible();
    await dialog.getByRole('button', { name: 'Copy Registries' }).click();
    await expect(page.getByText('Registry list copied to clipboard!')).toBeVisible();
    expect(await clipboard(page)).toBe('quay.io/openshift-release-dev\nregistry.redhat.io/redhat');
  });

  test('images tab lists, filters and copies full image references', async ({ page }) => {
    const dialog = await openImageSources(page);
    await dialog.getByRole('tab', { name: 'Images (3)' }).click();
    await expect(dialog.getByRole('listitem')).toHaveText(images);
    await dialog.getByRole('button', { name: 'Copy Images' }).click();
    expect(await clipboard(page)).toBe(images.join('\n'));

    await dialog.getByRole('textbox', { name: 'Filter images' }).fill('operator-index');
    await expect(dialog.getByRole('listitem')).toHaveCount(2);
    await dialog.getByRole('button', { name: 'Copy Filtered Images' }).click();
    await expect(page.getByText('Image list copied to clipboard!').first()).toBeVisible();
    expect(await clipboard(page)).toBe(images.slice(1).join('\n'));
  });
});

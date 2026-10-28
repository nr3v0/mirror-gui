import { test, expect, type Page, type Route } from '@playwright/test';

interface Status {
  selected: 'built-in' | 'redhat';
  source: string;
  version: string;
  builtInSource: string;
  redhatDownloaded: boolean;
  switching: boolean;
}

const builtIn: Status = {
  selected: 'built-in', source: 'source', version: '2.0.0', builtInSource: 'source', redhatDownloaded: false, switching: false,
};
const redHat: Status = { ...builtIn, selected: 'redhat', source: 'redhat-runtime', version: '4.20.0', redhatDownloaded: true };

/** Mocks the Dashboard's system endpoints so they report `current()`. */
async function mockOcMirror(page: Page, current: () => Status) {
  await page.route('**/api/system/status', route => route.fulfill({
    json: { ocMirrorVersion: current().version, systemHealth: 'healthy', pullSecretDetected: true },
  }));
  await page.route('**/api/system/info', async route => {
    const res = await route.fetch();
    route.fulfill({ response: res, json: { ...(await res.json()), ocMirrorSource: current().source } });
  });
}

async function openDialog(page: Page, version: string) {
  await page.goto('/');
  await page.getByRole('button', { name: `oc-mirror ${version}: choose binary` }).click();
  const dialog = page.getByRole('dialog', { name: 'oc-mirror binary' });
  await expect(dialog.getByText(`oc-mirror ${version}`)).toBeVisible();
  return dialog;
}

test.describe('oc-mirror binary selection', () => {
  test('the version opens a dialog with both binaries; Save needs a change', async ({ page }) => {
    await mockOcMirror(page, () => builtIn);
    await page.route('**/api/system/oc-mirror', route => route.fulfill({ json: builtIn }));
    const dialog = await openDialog(page, '2.0.0');

    await expect(dialog.getByRole('radio', { name: 'Build from source' })).toBeChecked();
    await expect(dialog.getByRole('radio', { name: 'Official Red Hat Download' })).not.toBeChecked();
    await expect(dialog.getByText('Downloaded when selected (needs internet access)')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Save' })).toBeDisabled();
    await dialog.getByRole('radio', { name: 'Official Red Hat Download' }).check();
    await expect(dialog.getByRole('button', { name: 'Save' })).toBeEnabled();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
  });

  test('selecting the Red Hat download downloads it and updates the dashboard', async ({ page }) => {
    let current = builtIn;
    let release: () => void = () => {};
    const downloaded = new Promise<void>(resolve => { release = resolve; });
    const puts: unknown[] = [];
    await mockOcMirror(page, () => current);
    await page.route('**/api/system/oc-mirror', async (route: Route) => {
      if (route.request().method() === 'PUT') {
        puts.push(route.request().postDataJSON());
        await downloaded;
        current = redHat;
      }
      return route.fulfill({ json: current });
    });

    const dialog = await openDialog(page, '2.0.0');
    await dialog.getByRole('radio', { name: 'Official Red Hat Download' }).check();
    await dialog.getByRole('button', { name: 'Save' }).click();
    await expect(dialog.getByRole('button', { name: /Downloading/ })).toBeDisabled();
    await expect(dialog.getByRole('radio', { name: 'Build from source' })).toBeDisabled();
    release();

    await expect(dialog).toBeHidden();
    expect(puts).toEqual([{ binary: 'redhat' }]);
    await expect(page.getByText('Now using Official Red Hat Download (oc-mirror 4.20.0) for new operations')).toBeVisible();
    await expect(page.getByRole('button', { name: 'oc-mirror 4.20.0: choose binary' })).toBeVisible();
    await expect(page.getByText('Red Hat build (downloaded)')).toBeVisible();

    const again = await openDialog(page, '4.20.0');
    await expect(again.getByRole('radio', { name: 'Official Red Hat Download' })).toBeChecked();
    await expect(again.getByText('Downloaded', { exact: true })).toBeVisible();
  });

  test('a failed download is reported and the dialog stays open', async ({ page }) => {
    await mockOcMirror(page, () => builtIn);
    await page.route('**/api/system/oc-mirror', route => route.request().method() === 'PUT'
      ? route.fulfill({ status: 502, json: { error: "Could not use Red Hat's oc-mirror: dial tcp: no route to host" } })
      : route.fulfill({ json: builtIn }));

    const dialog = await openDialog(page, '2.0.0');
    await dialog.getByRole('radio', { name: 'Official Red Hat Download' }).check();
    await dialog.getByRole('button', { name: 'Save' }).click();
    await expect(dialog.getByText("Could not use Red Hat's oc-mirror: dial tcp: no route to host")).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Save' })).toBeEnabled();
    await expect(dialog.getByRole('radio', { name: 'Official Red Hat Download' })).toBeChecked();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(page.getByRole('button', { name: 'oc-mirror 2.0.0: choose binary' })).toBeVisible();
  });

  test('an image built with Red Hat\'s oc-mirror labels the built-in option accordingly', async ({ page }) => {
    const status: Status = { ...builtIn, source: 'redhat', builtInSource: 'redhat' };
    await mockOcMirror(page, () => status);
    await page.route('**/api/system/oc-mirror', route => route.fulfill({ json: status }));
    const dialog = await openDialog(page, '2.0.0');
    await expect(dialog.getByRole('radio', { name: 'Built-in Red Hat build' })).toBeChecked();
    await expect(dialog.getByRole('radio', { name: 'Build from source' })).toHaveCount(0);
  });
});

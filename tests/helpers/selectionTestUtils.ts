import { expect, type Page, type Locator } from '@playwright/test';

type Container = Page | Locator;

/**
 * Asserts that a row checkbox toggles on click (check → uncheck).
 */
export async function assertRowCheckboxToggles(table: Locator, configName: string): Promise<void> {
  const row = table.locator('tbody tr', { hasText: configName }).first();
  const cb = row.locator('input[type="checkbox"]');
  await expect(cb).not.toBeChecked();
  await cb.click();
  await expect(cb).toBeChecked();
  await cb.click();
  await expect(cb).not.toBeChecked();
}

/**
 * Checks two non-adjacent rows by config name and asserts the third is unchecked.
 */
export async function assertMultiRowSelect(
  table: Locator,
  checkNames: [string, string],
  uncheckedName: string,
): Promise<void> {
  for (const name of checkNames) {
    await table.locator('tbody tr', { hasText: name }).first().locator('input[type="checkbox"]').click();
  }
  for (const name of checkNames) {
    await expect(table.locator('tbody tr', { hasText: name }).first().locator('input[type="checkbox"]')).toBeChecked();
  }
  await expect(table.locator('tbody tr', { hasText: uncheckedName }).first().locator('input[type="checkbox"]')).not.toBeChecked();
}

/**
 * Clicks select-all, asserts all scoped rows are checked, clicks again,
 * asserts all are unchecked.
 */
export async function assertSelectAllToggles(table: Locator, configNames: string[]): Promise<void> {
  const selectAll = table.locator('thead input[type="checkbox"]');

  await selectAll.click();
  for (const name of configNames) {
    await expect(table.locator('tbody tr', { hasText: name }).first().locator('input[type="checkbox"]')).toBeChecked();
  }

  await selectAll.click();
  for (const name of configNames) {
    await expect(table.locator('tbody tr', { hasText: name }).first().locator('input[type="checkbox"]')).not.toBeChecked();
  }
}

/**
 * Checks two rows, asserts "Delete Selected (2)" is visible, unchecks both,
 * asserts button is hidden.
 */
export async function assertDeleteSelectedButtonVisibility(
  table: Locator,
  container: Container,
  checkNames: [string, string],
): Promise<void> {
  await expect(container.getByRole('button', { name: /^Delete Selected \(\d+\)$/ })).not.toBeVisible();

  for (const name of checkNames) {
    await table.locator('tbody tr', { hasText: name }).first().locator('input[type="checkbox"]').click();
  }
  await expect(container.getByRole('button', { name: /^Delete Selected \(2\)$/ })).toBeVisible();

  for (const name of checkNames) {
    await table.locator('tbody tr', { hasText: name }).first().locator('input[type="checkbox"]').click();
  }
  await expect(container.getByRole('button', { name: /^Delete Selected \(\d+\)$/ })).not.toBeVisible();
}

/**
 * Checks one row, clicks "Delete Selected", cancels the modal, asserts
 * every seeded row is still present.
 */
export async function assertCancelModalKeepsRows(
  page: Page,
  table: Locator,
  container: Container,
  configNames: string[],
  modalLabel: string,
): Promise<void> {
  const row = table.locator('tbody tr', { hasText: configNames[0] }).first();
  await row.locator('input[type="checkbox"]').click();
  await container.getByRole('button', { name: /^Delete Selected \(1\)$/ }).click();

  const modal = page.locator(`[aria-label="${modalLabel}"]`);
  await expect(modal).toBeVisible({ timeout: 5000 });
  await modal.getByRole('button', { name: 'Cancel' }).click();
  await expect(modal).not.toBeVisible();
  for (const name of configNames) {
    await expect(table.locator('tbody tr', { hasText: name }).first()).toBeVisible();
  }
}

/**
 * Checks one row by config name, confirms deletion, asserts that row is gone
 * while every unchecked seeded row survives.
 */
export async function assertDeleteSelectedRemovesOnlyChecked(
  page: Page,
  table: Locator,
  container: Container,
  deleteName: string,
  survivorNames: string[],
  modalLabel: string,
): Promise<void> {
  await table.locator('tbody tr', { hasText: deleteName }).first().locator('input[type="checkbox"]').click();
  await container.getByRole('button', { name: /^Delete Selected \(1\)$/ }).click();

  const modal = page.locator(`[aria-label="${modalLabel}"]`);
  await expect(modal).toBeVisible({ timeout: 5000 });
  await modal.getByRole('button', { name: 'Delete' }).click();

  await expect(modal).not.toBeVisible({ timeout: 10000 });
  await expect(table.locator('tbody tr', { hasText: deleteName })).not.toBeVisible({ timeout: 15000 });
  for (const name of survivorNames) {
    await expect(table.locator('tbody tr', { hasText: name }).first()).toBeVisible();
  }
}

/**
 * Verifies the table contains exactly the remaining seeded rows, selects all
 * via the header checkbox, confirms deletion, and asserts the empty state.
 *
 * The pre-delete row-count check ensures the suite only deletes its own data,
 * so the empty state genuinely belongs to this suite — not to a coincidental
 * wipe of history left by a previous local run.
 */
export async function assertSelectAllDeleteRemovesAll(
  page: Page,
  table: Locator,
  container: Container,
  remainingConfigNames: string[],
  modalLabel: string,
  emptyStateContainer: Container,
): Promise<void> {
  for (const name of remainingConfigNames) {
    await expect(table.locator('tbody tr', { hasText: name }).first()).toBeVisible();
  }
  const checkboxCount = await table.locator('tbody input[type="checkbox"]').count();
  expect(checkboxCount).toBe(remainingConfigNames.length);

  await table.locator('thead input[type="checkbox"]').click();
  await container.getByRole('button', { name: /^Delete Selected \(\d+\)$/ }).click();

  const modal = page.locator(`[aria-label="${modalLabel}"]`);
  await expect(modal).toBeVisible({ timeout: 5000 });
  await modal.getByRole('button', { name: 'Delete' }).click();

  await expect(modal).not.toBeVisible({ timeout: 10000 });
  for (const name of remainingConfigNames) {
    await expect(table.locator('tbody tr', { hasText: name })).not.toBeVisible({ timeout: 15000 });
  }
  await expect(emptyStateContainer.getByText('No operations found.')).toBeVisible({ timeout: 15000 });
}

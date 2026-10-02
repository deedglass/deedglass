// The kit's Mirror: Play Sealed closes the halves and fades the ring; reduced motion jumps to the end.
import { AUTH, expect, test } from './fixtures.ts';

test.use({ storageState: AUTH('owner') });

test('Play Sealed and Play Mismatch work; reduced motion shows the end state at once', async ({ page }) => {
  await page.goto('/app/_kit');
  const svg = page.locator('#kit-player .dg-mirror').last();
  await page.getByRole('button', { name: 'Play Sealed' }).click();
  await expect(svg).toHaveAttribute('data-state', 'sealed');
  await page.waitForTimeout(700);
  expect(await svg.locator('.half.right').getAttribute('transform')).toBe('translate(1.5 0)');
  await page.getByRole('button', { name: 'Play Mismatch' }).click();
  await page.waitForTimeout(500);
  await expect(svg).toHaveAttribute('data-state', 'mismatch');
  expect(await svg.locator('.half.right').getAttribute('transform')).toBe('translate(2.5 0)');
  await expect(svg.locator('.hairline')).toHaveClass(/on/);
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.getByRole('button', { name: 'Play Sealed' }).click();
  await page.waitForTimeout(30);
  expect(await svg.locator('.half.right').getAttribute('transform')).toBe('translate(1.5 0)');
});

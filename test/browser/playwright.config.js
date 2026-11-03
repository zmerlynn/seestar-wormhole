// The browser tests of browse's page and the app's page. They need a
// running browse on the Samba fixture's browse target, with BROWSE_URL the
// URL it prints, and APP_BIN, the built seestar-copy, which each app test
// starts itself.
const { defineConfig, devices } = require('@playwright/test');

if (!process.env.BROWSE_URL) {
  throw new Error('Set BROWSE_URL to the URL that seestar-copy-cli browse prints.');
}
if (!process.env.APP_BIN) {
  throw new Error('Set APP_BIN to a built seestar-copy.');
}

module.exports = defineConfig({
  testDir: './tests',
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: process.env.BROWSE_URL,
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    { name: 'webkit', use: { ...devices['Desktop Safari'] } },
  ],
});

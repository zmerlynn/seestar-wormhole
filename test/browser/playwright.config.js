// The browser tests of browse's page. They need a running browse on the
// Samba fixture's browse target, with BROWSE_URL the URL it prints.
//
// app.spec.js, copy.spec.js and free.spec.js drove the standalone
// seestar-copy app's page, which the merged app replaced. They wait to be
// retargeted at the merged page and are not run: nothing yet serves that
// page without a window. They used APP_BIN, a built app, and CLI_BIN, a
// built seestar-copy-cli.
const { defineConfig, devices } = require('@playwright/test');

if (!process.env.BROWSE_URL) {
  throw new Error('Set BROWSE_URL to the URL that seestar-copy-cli browse prints.');
}

module.exports = defineConfig({
  testDir: './tests',
  testMatch: ['browse.spec.js', 'screenshots.spec.js'],
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

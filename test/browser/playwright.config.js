// The browser tests of browse's page and the app's page. They need a
// running browse on the Samba fixture's browse target, with BROWSE_URL the
// URL it prints; UI_TEST_BIN, internal/ui's test binary, whose
// TestBrowserServer each app test starts to serve the page without a
// window; and CLI_BIN, a built seestar-wormhole-cli, whose sync holds the folder in use.
const { defineConfig, devices } = require('@playwright/test');

if (!process.env.BROWSE_URL) {
  throw new Error('Set BROWSE_URL to the URL that seestar-wormhole-cli browse prints.');
}
if (!process.env.UI_TEST_BIN) {
  throw new Error('Set UI_TEST_BIN to internal/ui\'s test binary (go test -c ./internal/ui).');
}
if (!process.env.CLI_BIN) {
  throw new Error('Set CLI_BIN to a built seestar-wormhole-cli.');
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

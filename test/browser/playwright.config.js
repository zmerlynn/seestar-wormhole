// The browser tests of browse's page. They need a running browse on the
// Samba fixture's browse target; BROWSE_URL is the URL it prints.
const { defineConfig, devices } = require('@playwright/test');

if (!process.env.BROWSE_URL) {
  throw new Error('Set BROWSE_URL to the URL that seestar-copy-cli browse prints.');
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

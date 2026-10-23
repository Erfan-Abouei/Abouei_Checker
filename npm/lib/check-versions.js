'use strict';

// Fails when the npm version drifts from the repository VERSION file.
// Run: npm run check-versions (also wired into CI).

const fs = require('fs');
const path = require('path');

const root = path.join(__dirname, '..', '..');
const repoVersion = fs.readFileSync(path.join(root, 'VERSION'), 'utf8').trim();
const pkg = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'package.json'), 'utf8'));

if (pkg.version !== repoVersion) {
  console.error(`version mismatch: npm/package.json=${pkg.version} VERSION=${repoVersion}`);
  process.exit(1);
}
console.log(`versions aligned: ${pkg.version}`);

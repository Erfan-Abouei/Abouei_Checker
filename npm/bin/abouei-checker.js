#!/usr/bin/env node
'use strict';

// abouei-checker launcher: dispatch to the provisioned Go binary, forwarding
// arguments, stdio, signals, and the exit code. The Go application owns all
// behavior (including the default TUI when interactive); this file owns
// only process plumbing.

const { spawnSync } = require('child_process');
const fs = require('fs');
const path = require('path');

const { resolve } = require('../lib/platform');
const { binaryPath } = require('../lib/install');

function findBinary() {
  // Explicit override for tests and advanced setups. The versioned
  // provisioned binary remains the default.
  if (process.env.ABOUEI_CHECKER_BINARY) {
    return process.env.ABOUEI_CHECKER_BINARY;
  }
  const entry = resolve();
  if (!entry) {
    const { unsupportedMessage } = require('../lib/platform');
    console.error(unsupportedMessage());
    process.exit(1);
  }
  return binaryPath(entry);
}

function main() {
  const binary = findBinary();
  if (!fs.existsSync(binary)) {
    console.error(
      `abouei-checker: binary not found at ${binary}.\n` +
        'The postinstall download probably failed or was skipped.\n' +
        'Run `npm rebuild abouei-checker` (or reinstall the package) to provision it.\n' +
        'Pre-release note: installable binaries appear once GitHub Release artifacts exist;',
    );
    console.error('see https://github.com/Erfan-Abouei/Abouei_Checker/releases.');
    process.exit(1);
  }
  const args = process.argv.slice(2);
  const result = spawnSync(binary, args, { stdio: 'inherit' });
  if (result.error) {
    console.error(`abouei-checker: failed to start: ${result.error.message}`);
    process.exit(1);
  }
  // Propagate signals faithfully; otherwise mirror the exit status.
  if (result.signal) {
    process.kill(process.pid, result.signal);
    return;
  }
  process.exit(result.status === null || result.status === undefined ? 1 : result.status);
}

main();

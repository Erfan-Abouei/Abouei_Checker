'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

const platform = require('../lib/platform');

test('supported platforms resolve to deterministic Go targets', () => {
  const expectations = {
    'linux-x64': 'linux-amd64',
    'linux-arm64': 'linux-arm64',
    'darwin-x64': 'darwin-amd64',
    'darwin-arm64': 'darwin-arm64',
    'win32-x64': 'windows-amd64',
  };
  for (const [key, want] of Object.entries(expectations)) {
    const [osName, arch] = key.split('-');
    const entry = platform.resolve(osName, arch);
    assert.ok(entry, `${key} must resolve`);
    assert.equal(`${entry.goos}-${entry.goarch}`, want);
  }
});

test('unsupported platforms resolve to null with a useful message', () => {
  assert.equal(platform.resolve('win32', 'arm64'), null);
  assert.equal(platform.resolve('freebsd', 'x64'), null);
  const message = platform.unsupportedMessage('win32', 'arm64');
  assert.match(message, /unsupported platform/);
  assert.match(message, /Supported platforms/);
  assert.match(message, /build from source/);
});

test('asset names and URLs are version-pinned, never latest', () => {
  const entry = platform.resolve('linux', 'x64');
  assert.equal(platform.assetName('0.6.0', entry), 'abouei-checker_0.6.0_linux-amd64.tar.gz');
  const url = platform.downloadUrl({
    owner: 'Erfan-Abouei',
    repo: 'Abouei_Checker',
    version: '0.6.0',
    entry,
  });
  assert.equal(
    url,
    'https://github.com/Erfan-Abouei/Abouei_Checker/releases/download/v0.6.0/abouei-checker_0.6.0_linux-amd64.tar.gz',
  );
  assert.doesNotMatch(url, /latest/);
});

test('launcher forwards args and exit codes via override binary', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'abouei-launcher-'));
  const fake = path.join(dir, 'fake.sh');
  fs.writeFileSync(fake, '#!/bin/sh\necho "args:$@"\nexit 7\n', { mode: 0o755 });
  const launcher = path.join(__dirname, '..', 'bin', 'abouei-checker.js');
  const result = spawnSync(process.execPath, [launcher, 'version', '--output', 'json'], {
    env: { ...process.env, ABOUEI_CHECKER_BINARY: fake },
    encoding: 'utf8',
  });
  assert.equal(result.status, 7, `exit code must propagate (stderr: ${result.stderr})`);
  assert.match(result.stdout, /args:version --output json/);
  fs.rmSync(dir, { recursive: true, force: true });
});

test('launcher fails clearly when the binary is missing', () => {
  const launcher = path.join(__dirname, '..', 'bin', 'abouei-checker.js');
  const result = spawnSync(process.execPath, [launcher, 'version'], {
    env: {
      ...process.env,
      ABOUEI_CHECKER_BINARY: path.join(os.tmpdir(), 'abouei-checker-definitely-missing-binary'),
    },
    encoding: 'utf8',
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /binary not found/);
});

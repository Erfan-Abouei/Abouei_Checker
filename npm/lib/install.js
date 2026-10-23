'use strict';

// Post-install provisioning: download the pinned, checksummed release
// binary for this platform. HTTPS-only, version-pinned, SHA-256 verified.
// Never executes a partially downloaded or unverified binary.

const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const https = require('https');
const { execFileSync } = require('child_process');

const { resolve, downloadUrl, unsupportedMessage } = require('./platform');

const OWNER = 'Erfan-Abouei';
const REPO = 'Abouei_Checker';

function packageVersion() {
  const pkg = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'package.json'), 'utf8'));
  return pkg.version;
}

function binariesDir() {
  return path.join(__dirname, '..', 'binaries');
}

function binaryName(entry) {
  return `abouei-checker${entry.ext}`;
}

function binaryPath(entry) {
  return path.join(binariesDir(), binaryName(entry));
}

function get(url, dest) {
  return new Promise((resolvePromise, rejectPromise) => {
    const out = fs.createWriteStream(dest, { mode: 0o600 });
    const req = https.get(
      url,
      { headers: { 'User-Agent': 'abouei-checker-npm-installer' } },
      (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          out.close();
          fs.unlink(dest, () => {});
          resolvePromise(get(res.headers.location, dest));
          return;
        }
        if (res.statusCode !== 200) {
          out.close();
          fs.unlink(dest, () => {});
          rejectPromise(new Error(`download failed: HTTP ${res.statusCode} for ${redactUrl(url)}`));
          return;
        }
        res.pipe(out);
        out.on('finish', () => {
          out.close();
          resolvePromise();
        });
      },
    );
    req.on('error', (err) => {
      out.close();
      fs.unlink(dest, () => {});
      rejectPromise(new Error(`download failed for ${redactUrl(url)}: ${err.message}`));
    });
    req.setTimeout(120000, () => {
      req.destroy(new Error('download timed out after 120s'));
    });
  });
}

function redactUrl(url) {
  // Release URLs carry no secrets, but keep the helper so future query
  // parameters (tokens) can never leak into install errors.
  return String(url).split('?')[0];
}

function sha256File(file) {
  return new Promise((resolvePromise, rejectPromise) => {
    const hash = crypto.createHash('sha256');
    const stream = fs.createReadStream(file);
    stream.on('data', (chunk) => hash.update(chunk));
    stream.on('end', () => resolvePromise(hash.digest('hex')));
    stream.on('error', rejectPromise);
  });
}

async function verifyChecksum(archivePath, checksumUrl) {
  const tmpChecksum = `${archivePath}.expected-sha256`;
  await get(checksumUrl, tmpChecksum);
  const raw = fs.readFileSync(tmpChecksum, 'utf8').trim().split(/\s+/)[0];
  fs.unlink(tmpChecksum, () => {});
  if (!/^[0-9a-f]{64}$/i.test(raw)) {
    throw new Error('checksum file is malformed; refusing to install');
  }
  const actual = await sha256File(archivePath);
  if (actual.toLowerCase() !== raw.toLowerCase()) {
    throw new Error(`checksum mismatch for ${path.basename(archivePath)}; refusing to install`);
  }
}

function extract(archivePath, destDir, entry) {
  fs.mkdirSync(destDir, { recursive: true });
  // The release archive contains exactly one file: the platform binary.
  // Extract it under a fixed name so the launcher path is deterministic.
  execFileSync('tar', ['-xzf', archivePath, '-C', destDir], { stdio: 'pipe' });
  const extracted = fs.readdirSync(destDir).filter((f) => f !== path.basename(archivePath));
  if (extracted.length !== 1) {
    throw new Error(`unexpected archive contents (${extracted.length} files); refusing to install`);
  }
  const finalPath = binaryPath(entry);
  fs.renameSync(path.join(destDir, extracted[0]), finalPath);
  if (process.platform !== 'win32') {
    fs.chmodSync(finalPath, 0o755);
  }
}

async function install() {
  if (process.env.ABOUEI_CHECKER_SKIP_POSTINSTALL === '1') {
    console.log('abouei-checker: postinstall skipped (ABOUEI_CHECKER_SKIP_POSTINSTALL=1).');
    return;
  }
  const entry = resolve();
  if (!entry) {
    throw new Error(unsupportedMessage());
  }
  const version = packageVersion();
  const url = downloadUrl({ owner: OWNER, repo: REPO, version, entry });
  const dir = binariesDir();
  fs.mkdirSync(dir, { recursive: true });
  const target = binaryPath(entry);
  if (fs.existsSync(target)) {
    console.log(`abouei-checker: binary already present for ${entry.key}, skipping download.`);
    return;
  }
  const archive = path.join(dir, `abouei-checker_${version}_${entry.goos}-${entry.goarch}.tar.gz`);
  console.log(`abouei-checker: downloading ${version} for ${entry.key} ...`);
  await get(url, archive);
  try {
    await verifyChecksum(archive, `${url}.sha256`);
    extract(archive, dir, entry);
  } finally {
    fs.unlink(archive, () => {});
  }
  console.log(`abouei-checker: installed ${target}`);
}

if (require.main === module) {
  install().catch((err) => {
    console.error(`abouei-checker postinstall failed: ${err.message}`);
    console.error('The package installed without a binary. Check your network, verify the');
    console.error('platform support matrix, then run `npm rebuild abouei-checker` to retry.');
    process.exit(1);
  });
}

module.exports = { install, binariesDir, binaryPath, binaryName, packageVersion };

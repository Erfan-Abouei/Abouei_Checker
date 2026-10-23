'use strict';

// Platform resolution for the abouei-checker Go binary. The single source
// of truth for supported targets is scripts/cross-build.sh; this table must
// stay aligned with it. Unsupported platforms fail with a diagnostic that
// tells the user exactly what to do instead.

const PLATFORMS = {
  'linux-x64': { goos: 'linux', goarch: 'amd64', ext: '', gz: true },
  'linux-arm64': { goos: 'linux', goarch: 'arm64', ext: '', gz: true },
  'darwin-x64': { goos: 'darwin', goarch: 'amd64', ext: '', gz: true },
  'darwin-arm64': { goos: 'darwin', goarch: 'arm64', ext: '', gz: true },
  'win32-x64': { goos: 'windows', goarch: 'amd64', ext: '.exe', gz: true },
};

function currentPlatformKey(platform = process.platform, arch = process.arch) {
  return `${platform}-${arch}`;
}

// resolve returns the asset descriptor for a platform/arch pair, or null
// when the combination is not distributed. Pure function for testability.
function resolve(platform = process.platform, arch = process.arch) {
  const key = currentPlatformKey(platform, arch);
  const entry = PLATFORMS[key];
  if (!entry) {
    return null;
  }
  return { key, ...entry };
}

// assetName builds the deterministic release artifact name for a version
// and platform entry. It must match the release workflow's archive step.
function assetName(version, entry) {
  return `abouei-checker_${version}_${entry.goos}-${entry.goarch}.tar.gz`;
}

// downloadUrl pins an exact versioned artifact. "latest" is never used:
// installation and execution must be deterministic.
function downloadUrl({ owner, repo, version, entry }) {
  const tag = `v${version}`;
  return `https://github.com/${owner}/${repo}/releases/download/${tag}/${assetName(version, entry)}`;
}

function unsupportedMessage(platform = process.platform, arch = process.arch) {
  const supported = Object.keys(PLATFORMS).join(', ');
  return (
    `abouei-checker: unsupported platform "${platform}-${arch}".\n` +
    `Supported platforms: ${supported}.\n` +
    `Options: build from source (https://github.com/Erfan-Abouei/Abouei_Checker/blob/main/DEVELOPMENT.md) ` +
    `or use Docker (https://github.com/Erfan-Abouei/Abouei_Checker/blob/main/README.md#docker).`
  );
}

module.exports = {
  PLATFORMS,
  currentPlatformKey,
  resolve,
  assetName,
  downloadUrl,
  unsupportedMessage,
};

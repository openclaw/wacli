import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const releaseWorkflow = readFileSync(`${root}/.github/workflows/release.yml`, 'utf8');
const darwinConfig = readFileSync(`${root}/.goreleaser.yaml`, 'utf8');
const releaseDocs = readFileSync(`${root}/docs/release.md`, 'utf8');

test('release caller pins the fleet v1 split-host workflow', () => {
  assert.match(releaseWorkflow, /uses: openclaw\/release-workflows\/\.github\/workflows\/release-go-cli\.yml@v1/);
  assert.match(releaseWorkflow, /split-goreleaser-config: \.goreleaser-linux-windows\.yaml/);
  assert.match(releaseWorkflow, /reproducible-rebuild: non-darwin/);
  assert.match(releaseWorkflow, /stable-identifier: org\.openclaw\.wacli/);
  assert.match(releaseWorkflow, /checksum-filename: checksums\.txt/);
  assert.match(releaseWorkflow, /archive-files: '\["LICENSE","README\.md"\]'/);
});

test('release caller maps every required repository secret', () => {
  for (const secret of [
    'MACOS_SIGNING_P12',
    'MACOS_SIGNING_P12_PASSWORD',
    'ASC_KEY_ID',
    'ASC_ISSUER_ID',
    'ASC_PRIVATE_KEY_P8',
  ]) {
    assert.ok(releaseWorkflow.includes(`${secret}: \${{ secrets.${secret} }}`));
  }
  assert.match(releaseWorkflow, /TAP_TOKEN: \$\{\{ secrets\.HOMEBREW_TAP_TOKEN \}\}/);
});

test('shared workflow owns universal assembly and native verification', () => {
  assert.doesNotMatch(darwinConfig, /^universal_binaries:/m);
  assert.equal(existsSync(`${root}/.github/workflows/release-verify.yml`), false);
  assert.doesNotMatch(releaseDocs, /release-local\.mjs|NOTARYTOOL_KEYCHAIN_PROFILE|confirm-gatekeeper-vm/);
});

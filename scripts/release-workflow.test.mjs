import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import {
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath, pathToFileURL } from 'node:url';

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

test('Darwin builds reject a missing or changed minimum on any slice', (t) => {
  assert.match(darwinConfig, /post: bash scripts\/check-macos-minimum\.sh "\{\{ \.Path \}\}"/);
  const fixture = realpathSync(mkdtempSync(path.join(tmpdir(), 'wacli-macos-minimum-')));
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  writeFileSync(path.join(fixture, 'lipo'), '#!/bin/sh\nprintf "%s\\n" "$TEST_ARCHS"\n', { mode: 0o755 });
  writeFileSync(path.join(fixture, 'otool'), `#!/bin/sh
if [ "$2" = arm64 ]; then minimum=$TEST_ARM_MINIMUM; else minimum=15.0; fi
printf '      cmd LC_BUILD_VERSION\\n  cmdsize 32\\n platform 1\\n'
if [ -n "$minimum" ]; then printf '    minos %s\\n' "$minimum"; fi
printf '      sdk 26.0\\n'
`, { mode: 0o755 });
  const check = (minimum, architectures = 'x86_64 arm64') => spawnSync('bash', [
    path.join(root, 'scripts/check-macos-minimum.sh'), 'synthetic-binary',
  ], {
    encoding: 'utf8',
    env: {
      ...process.env,
      PATH: `${fixture}${path.delimiter}${process.env.PATH}`,
      TEST_ARCHS: architectures,
      TEST_ARM_MINIMUM: minimum,
    },
  });
  assert.equal(check('15.0').status, 0);
  for (const minimum of ['26.0', '14.0', '']) {
    const result = check(minimum);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /Expected macOS 15\.0 minimum.*arm64/);
  }
  assert.notEqual(check('15.0', '').status, 0);
});

test('Darwin preparation requires a literally matching dated changelog heading', async (t) => {
  const fixture = realpathSync(mkdtempSync(path.join(tmpdir(), 'wacli-release-source-')));
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  cpSync(path.join(root, 'scripts'), path.join(fixture, 'scripts'), { recursive: true });
  mkdirSync(path.join(fixture, 'cmd/wacli'), { recursive: true });
  writeFileSync(path.join(fixture, 'cmd/wacli/root.go'), 'const sourceVersion = "0.17.2"\n');
  const goMod = readFileSync(path.join(root, 'go.mod'), 'utf8');
  writeFileSync(path.join(fixture, 'go.mod'), goMod);
  const { prepareDarwinRelease } = await import(
    pathToFileURL(path.join(fixture, 'scripts/release-local.mjs')).href
  );
  const commit = 'a'.repeat(40);

  for (const [name, heading, accepted] of [
    ['exact dated version', '## 0.17.2 - 2026-09-07', true],
    ['letter separators', '## 0x17y2 - 2026-09-07', false],
    ['digit separators', '## 001702 - 2026-09-07', false],
    ['different version', '## 0.17.3 - 2026-09-07', false],
    ['undated version', '## 0.17.2', false],
    ['malformed date', '## 0.17.2 - 2026-9-7', false],
    ['date format without calendar validation', '## 0.17.2 - 2026-99-99', true],
  ]) {
    await t.test(name, () => {
      writeFileSync(path.join(fixture, 'CHANGELOG.md'), `# Changelog\n\n${heading}\n\n- Fix.\n`);
      const sourceAccepted = new Error('source validation passed');
      const executionCalls = [];
      const run = (command, args, options) => {
        assert.equal(options.cwd, fixture);
        if (command === 'git') {
          if (args[0] === 'status' || args[0] === 'merge-base') return { stdout: '' };
          if (args[0] === 'rev-parse') return { stdout: commit };
          if (args[0] === 'show' && args[1] === `${commit}:go.mod`) return { stdout: goMod };
        }
        executionCalls.push([command, ...args]);
        throw sourceAccepted;
      };

      assert.throws(
        () => prepareDarwinRelease({
          tag: 'v0.17.2',
          commit,
          outputDir: path.join(fixture, 'candidate'),
          platform: 'darwin',
          env: {
            MAC_RELEASE_CODESIGN_IDENTITY: 'fixture-signing-identity',
            NOTARYTOOL_KEYCHAIN_PROFILE: 'fixture-notary-profile',
          },
          run,
        }),
        accepted
          ? (error) => error === sourceAccepted
          : /CHANGELOG\.md section 0\.17\.2 must be dated before official preparation/,
      );
      assert.deepEqual(executionCalls, accepted ? [['go', 'env', 'GOVERSION']] : []);
    });
  }
});


test('release caller forwards the optional nonpublishing dry run', () => {
  assert.match(releaseWorkflow, /dry_run:[\s\S]*?default: false[\s\S]*?type: boolean/);
  assert.match(releaseWorkflow, /dry-run: \$\{\{ inputs\.dry_run \}\}/);
});

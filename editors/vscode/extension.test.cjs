const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { findProject, toolPath, runExport, isSourceFile } = require('./runner.js');

test('finds enclosing projects and detects platform binaries without PATH', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'tabforge project '));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.mkdirSync(path.join(root, 'Tables', 'Nested'), { recursive: true });
  fs.writeFileSync(path.join(root, 'tabforge.json'), '{}');
  assert.equal(findProject(path.join(root, 'Tables', 'Nested')), path.join(root, 'tabforge.json'));
  fs.mkdirSync(path.join(root, 'Tools', 'TabForge'), { recursive: true });
  fs.writeFileSync(path.join(root, 'Tools', 'TabForge', 'tabforge.exe'), 'fixture');
  assert.equal(toolPath(root, '/missing', '', 'win32', 'x64'), path.join(root, 'Tools', 'TabForge', 'tabforge.exe'));
  assert.equal(toolPath('/missing', '/missing', '', 'darwin', 'arm64'), undefined);
});

test('uses literal arguments for paths containing shell characters', async t => {
  if (process.platform === 'win32') return t.skip('POSIX script fixture');
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'tabforge $(literal) '));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const tool = path.join(root, 'tool');
  fs.writeFileSync(tool, '#!/bin/sh\nprintf "%s" "$1"\n', { mode: 0o755 });
  const config = path.join(root, 'tabforge.json');
  let output = '';
  await runExport(tool, config, text => output += text).completion;
  assert.equal(output, '-project=' + config);
  await assert.rejects(runExport(path.join(root, 'missing'), config, () => {}).completion);
});

test('automatic export ignores generated replacement events, backups and unrelated files', () => {
  const root = path.resolve('fixture');
  const config = { output: 'Build/Generated', schema: { dir: 'Protocols', imports: ['Shared'] }, tables: [{ index: 'Tables/Index.csv', mapping: 'Config/mapping.json' }] };
  for (const file of ['tabforge.json', 'Protocols/common.proto', 'Shared/common.proto', 'Tables/Items.xlsx', 'Config/mapping.json']) assert.equal(isSourceFile(root, path.join(root, file), config), true, file);
  for (const file of ['Build/Generated/schema/types.ts', 'Build/Generated.tabforge-backup/data/tables.json', 'Build/.tabforge-stage-1/schema/bundle.json', 'Tables/~$Items.xlsx', 'Clients/package.json']) assert.equal(isSourceFile(root, path.join(root, file), config), false, file);
});

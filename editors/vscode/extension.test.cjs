const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { findProject, toolPath, runExport, isSourceFile } = require('./runner.js');
const { diagnosticEntries } = require('./diagnostics.js');

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

test('structured diagnostics locate Proto sources and describe workbook cells without opening binary text', () => {
  const config = path.resolve('project/tabforge.json');
  const proto = path.resolve('project/Protocols/data.proto');
  const workbook = path.resolve('project/Tables/Items.xlsx');
  const entries = diagnosticEntries({format:'tabforge.report.v1', diagnostics:[
    {code:'proto_compile',message:'syntax error',path:proto,line:4,column:8},
    {code:'UnknownEnumValue',message:'unknown enum',path:workbook,sheet:'Items',cell:'H3',line:3,column:8,hint:'Use EPIC'}
  ]}, config);
  assert.equal(entries[0].path, proto); assert.equal(entries[0].line, 3); assert.equal(entries[0].column, 7);
  assert.equal(entries[1].path, config); assert.match(entries[1].message, /Items.xlsx Items H3/); assert.match(entries[1].message, /Use EPIC/);
  assert.deepEqual(diagnosticEntries({},config), []);
  assert.deepEqual(diagnosticEntries({format:'tabforge.report.v1',diagnostics:[]},config), []);
});

test('diagnostic columns convert proto tab stops and CSV byte offsets to editor UTF-16 columns', () => {
  const config = path.resolve('project/tabforge.json');
  const proto = path.resolve('project/Protocols/data.proto');
  const csv = path.resolve('project/Tables/Items.csv');
  const files = { [proto]: 'syntax = "proto3";\r\n\tmessage Item {\n\t\tint32 id = 1 // 中文\n}\n', [csv]: '\uFEFFid,名称,稀有度\n1,剑😀,EPIC\n' };
  const entries = diagnosticEntries({format:'tabforge.report.v1', diagnostics:[
    // protocompile: runes, tabs advance to the next multiple of 8.
    {code:'proto_compile',message:'x',path:proto,line:2,column:9},
    {code:'proto_compile',message:'x',path:proto,line:3,column:33},
    // csv.Reader.FieldPos: UTF-8 byte columns, counting the BOM on line 1.
    {code:'csv',message:'x',path:csv,line:1,column:14},
    {code:'csv',message:'x',path:csv,line:2,column:11}
  ]}, config, file => files[file]);
  assert.deepEqual(entries.map(entry => entry.column), [1, 18, 6, 6]);
  assert.equal(diagnosticEntries({format:'tabforge.report.v1', diagnostics:[{code:'x',message:'x',path:csv,line:1,column:5}]}, config, () => { throw new Error('gone'); })[0].column, 4);
});

test('export on save watches every workbook extension the compiler discovers', async t => {
  const Module = require('node:module');
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'tabforge watch '));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.mkdirSync(path.join(root, 'Tables'));
  fs.writeFileSync(path.join(root, 'tabforge.json'), JSON.stringify({ tables: [{ discover: { dir: 'Tables' } }] }));
  let pattern, changed, started = 0;
  const vscode = {
    window: { createOutputChannel: () => ({ appendLine() {}, append() {}, show() {}, dispose() {} }) },
    languages: { createDiagnosticCollection: () => ({ delete() {}, set() {}, dispose() {} }) },
    commands: { registerCommand: () => ({ dispose() {} }) },
    Uri: { file: fsPath => ({ scheme: 'file', fsPath }) },
    workspace: {
      isTrusted: true,
      getConfiguration: () => ({ get: (name, fallback) => name === 'exportOnSave' ? true : name === 'toolPath' ? (started++, '') : fallback })
    }
  };
  const watcher = { dispose() {}, onDidChange: listener => (changed = listener, { dispose() {} }), onDidCreate: () => ({ dispose() {} }), onDidDelete: () => ({ dispose() {} }) };
  vscode.workspace.createFileSystemWatcher = glob => { pattern = glob; return watcher; };
  const load = Module._load;
  Module._load = function (request, ...rest) { return request === 'vscode' ? vscode : load.call(this, request, ...rest); };
  t.after(() => { Module._load = load; });
  delete require.cache[require.resolve('./extension.js')];
  const extension = require('./extension.js');
  extension.activate({ subscriptions: [], extensionPath: path.join(root, 'missing-extension') });
  t.after(() => extension.deactivate());
  const extensions = pattern.match(/\{(.*)\}/)[1].split(',');
  for (const ext of ['proto', 'csv', 'xlsx', 'xlsm', 'json']) assert(extensions.includes(ext), ext);
  changed(vscode.Uri.file(path.join(root, 'Tables', 'Items.xlsm')));
  await new Promise(resolve => setTimeout(resolve, 700));
  assert.equal(started, 1, 'a saved .xlsm workbook schedules an automatic export');
});

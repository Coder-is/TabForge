const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { argumentsFor, executable, sourceProject, run, cleanupOwnedLocks } = require('./runner');

test('detects flat and nested projects and chooses a platform binary', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'tabforge cocos 中文 '));
  t.after(() => fs.rmSync(root, { recursive:true, force:true }));
  assert.equal(sourceProject(root), path.join(root, 'TabForge'));
  fs.writeFileSync(path.join(root, 'tabforge.json'), '{}');
  assert.equal(sourceProject(root), root);
  fs.mkdirSync(path.join(root, 'Tools', 'TabForge'), {recursive:true});
  fs.writeFileSync(path.join(root, 'Tools', 'TabForge', 'tabforge.exe'), 'tool');
  assert.equal(executable(root, '/missing', 'win32', 'x64'), path.join(root, 'Tools', 'TabForge', 'tabforge.exe'));
  assert.equal(executable('/missing', '/missing', 'darwin', 'arm64'), undefined);
  const extension=path.join(root,'extension');
  fs.mkdirSync(path.join(extension,'bin','darwin-arm64'),{recursive:true});
  const native=path.join(extension,'bin','darwin-arm64','tabforge');fs.writeFileSync(native,'native fixture');
  assert.equal(executable(root,extension,'darwin','x64'),native,'an emulated editor can launch the native bundled compiler');
});

test('all editor actions use literal arguments and structured reports', () => {
  const root = path.resolve('project 中文 $(literal)');
  const args = argumentsFor(root, 'export');
  assert(args.includes('-editor_project=' + root));
  assert(args.includes('-report'));
  assert(argumentsFor(root, 'check').includes('-check'));
  assert(argumentsFor(root, 'init').includes('-init=' + path.join(root, 'TabForge')));
  assert(argumentsFor(root, 'import', '/some Generated 中文').includes('-import=/some Generated 中文'));
  assert.throws(() => argumentsFor(root, 'import'));
  assert.throws(() => argumentsFor(root, 'unknown'));
});

test('a missing process never reuses a previous success report', async t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'tabforge stale '));
  t.after(() => fs.rmSync(root, {recursive:true,force:true}));
  fs.writeFileSync(path.join(root, '.tabforge-report.json'), '{"format":"tabforge.report.v1","success":true}');
  await assert.rejects(run(path.join(root, 'missing'), root, []).completion);
  assert.equal(fs.existsSync(path.join(root, '.tabforge-report.json')), false);
});

test('cleanup removes only the exited child lock and preserves peer locks', t => {
  const root=fs.mkdtempSync(path.join(os.tmpdir(),'tabforge locks '));t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
  const owned=path.join(root,'owned');const peer=path.join(root,'peer');
  fs.writeFileSync(owned,'123\n');fs.writeFileSync(peer,'456\n');
  cleanupOwnedLocks(123,[owned,peer]);
  assert.equal(fs.existsSync(owned),false);assert.equal(fs.readFileSync(peer,'utf8'),'456\n');
});

test('forced cancellation releases the child project lock before the next run', async t => {
  if (process.platform === 'win32') return t.skip('POSIX process fixture');
  const root=fs.mkdtempSync(path.join(os.tmpdir(),'tabforge cancelled '));t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
  const tool=path.join(root,'tool');const lock=path.join(root,'.tabforge-export.lock');
  fs.writeFileSync(tool,'#!/bin/sh\necho $$ > .tabforge-export.lock\nwhile :; do :; done\n',{mode:0o755});
  let output=''; const job=run(tool,root,['-project='+root],text=>output+=text);
  t.after(()=>job.child.kill('SIGKILL'));
  const rejected=assert.rejects(job.completion);
  const deadline=Date.now()+10000;
  while(!fs.existsSync(lock) && job.child.exitCode===null && Date.now()<deadline) await new Promise(resolve=>setTimeout(resolve,10));
  assert.equal(fs.existsSync(lock),true,`lock was not acquired; exit=${job.child.exitCode}; output=${output}`);
  job.child.kill('SIGKILL');await rejected;
  assert.equal(fs.existsSync(lock),false);
});

const fs = require('fs');
const path = require('path');
const { spawn } = require('child_process');

function sourceProject(root) {
  return fs.existsSync(path.join(root, 'tabforge.json')) ? root : path.join(root, 'TabForge');
}

function executable(root, extension, platform = process.platform, arch = process.arch) {
  const name = platform === 'win32' ? 'tabforge.exe' : 'tabforge';
  const bundled=[...new Set([arch,'arm64','x64'])].map(cpu=>path.join(extension,'bin',`${platform}-${cpu}`,name));
  return [...bundled, path.join(sourceProject(root), 'Tools', 'TabForge', name)].find(file => fs.existsSync(file));
}

function argumentsFor(root, action, bundle) {
  const args = ['-report', '-editor=cocos', '-editor_project=' + root];
  if (action === 'init') args.push('-init=' + path.join(root, 'TabForge'));
  else if (action === 'import') {
    if (!bundle) throw new Error('Choose an exported Generated directory');
    args.push('-import=' + bundle);
  } else {
    args.push('-project=' + sourceProject(root));
    if (action === 'check') args.push('-check');
    else if (action !== 'export') throw new Error('Unknown TabForge action: ' + action);
  }
  return args;
}

function run(tool, root, args, onOutput = () => {}) {
  const reportPath = path.join(root, '.tabforge-report.json');
  fs.rmSync(reportPath, { force: true });
  const child = spawn(tool, args, { cwd: root, shell: false, windowsHide: true });
  child.stdout.setEncoding('utf8'); child.stderr.setEncoding('utf8');
  child.stdout.on('data', onOutput); child.stderr.on('data', onOutput);
  const completion = new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('close', code => {
      const sourceArg = args.find(arg => arg.startsWith('-project=') || arg.startsWith('-init='));
      let source = sourceArg?.slice(sourceArg.indexOf('=') + 1);
      try { if (source && fs.statSync(source).isFile()) source = path.dirname(source); } catch { /* A failed init may not have a directory. */ }
      cleanupOwnedLocks(child.pid, [path.join(root,'.tabforge-client.lock'), ...(source ? [path.join(source,'.tabforge-export.lock')] : [])]);
      let report;
      try { report = JSON.parse(fs.readFileSync(reportPath, 'utf8')); } catch { /* Logs still explain incompatible/missing tools. */ }
      if (code === 0 && report?.format === 'tabforge.report.v1' && report.success) resolve(report);
      else { const error = new Error('TabForge failed; inspect the report and console.'); error.report = report; reject(error); }
    });
  });
  return { child, completion };
}
// Call only after the child has exited; never remove another process's lock.
function cleanupOwnedLocks(pid, files) {
  if (!pid) return;
  for (const file of files) {
    try { if (fs.readFileSync(file,'utf8').trim() === String(pid)) fs.unlinkSync(file); } catch { /* No owned lock remains. */ }
  }
}
module.exports = { sourceProject, executable, argumentsFor, run, cleanupOwnedLocks };

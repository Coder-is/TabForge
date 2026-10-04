const path = require('path');
const { sourceProject, executable, argumentsFor, run } = require('./runner');
let job;
let unloading = false;
let status = {running:false, log:'在项目 TabForge/ 中维护源文件，或导入已有 Generated 包。'};
function append(text) { if (status.log.length < 1024 * 1024) status.log += text + '\n'; }

async function execute(action, bundle) {
  if (job) { console.warn('TabForge 正在执行，请等待完成。'); return; }
  const root = Editor.Project.path;
  const tool = executable(root, __dirname);
  if (!tool) throw new Error('找不到 TabForge 工具，请安装对应系统的插件包。');
  status = {running:true, log:'正在' + action + '\n'};
  job = run(tool, root, argumentsFor(root, action, bundle), text => { append(text.trimEnd()); console.log('[TabForge] ' + text.trimEnd()); });
  try {
    const report = await job.completion;
    if (report.editorOutput && action !== 'check' && !unloading) {
      try { await Editor.Message.request('asset-db', 'refresh-asset', 'db://assets/resources/tabforge'); }
      catch (error) { append('导入成功，资产刷新失败；请在资源面板手动刷新：' + error); }
    }
    append('成功：' + (report.editorOutput || report.output || '校验通过'));
    console.log('[TabForge] 操作成功。报告：' + path.join(root, '.tabforge-report.json'));
    return report;
  } catch (error) {
    for (const item of error.report?.diagnostics || []) { const text = `${item.path || ''} ${item.sheet || ''} ${item.cell || ''} ${item.message}\n${item.hint || ''}`; append(text); console.error('[TabForge] ' + text); }
    append(String(error));
    console.error(error);
    return error.report;
  } finally { job = undefined; status.running = false; }
}

exports.methods = {
  openPanel: () => Editor.Panel.open('tabforge'),
  queryStatus: () => status,
  cancel: () => job?.child.kill(),
  init: () => execute('init'),
  check: () => execute('check'),
  exportProject: () => execute('export'),
  async importBundle() {
    const result = await Editor.Dialog.select({ title: '选择导出的 Generated 目录', path: sourceProject(Editor.Project.path), type: 'directory' });
    if (!result.canceled && result.filePaths?.[0]) return execute('import', result.filePaths[0]);
  },
  openReport() { Editor.Shell.openPath(path.join(Editor.Project.path, '.tabforge-report.json')); }
};
exports.load = () => { unloading = false; };
exports.unload = () => { unloading = true; job?.child.kill(); };

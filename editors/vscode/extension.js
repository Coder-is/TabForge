const vscode = require('vscode');
const fs = require('node:fs');
const path = require('node:path');
const { findProject, toolPath, runExport, isSourceFile } = require('./runner.js');
const { diagnosticEntries } = require('./diagnostics.js');

const active = new Map();
const timers = new Map();
let stopping = false;

function activate(context) {
    stopping = false;
    const output = vscode.window.createOutputChannel('TabForge');
    const diagnostics = vscode.languages.createDiagnosticCollection('TabForge');
    const projectUris = new Map();
    context.subscriptions.push(output, diagnostics);

    function updateDiagnostics(config) {
        for (const uri of projectUris.get(config) || []) diagnostics.delete(uri);
        const groups = new Map();
        try {
            const report = JSON.parse(fs.readFileSync(path.join(path.dirname(config), '.tabforge-report.json'), 'utf8'));
            for (const entry of diagnosticEntries(report, config)) {
                const uri = vscode.Uri.file(entry.path);
                const diagnostic = new vscode.Diagnostic(new vscode.Range(entry.line, entry.column, entry.line, entry.column + 1), entry.message, vscode.DiagnosticSeverity.Error);
                diagnostic.code = entry.code; diagnostic.source = 'TabForge';
                if (!groups.has(entry.path)) groups.set(entry.path, {uri, items:[]});
                groups.get(entry.path).items.push(diagnostic);
            }
        } catch { /* An older tool still reports through the output channel. */ }
        for (const group of groups.values()) diagnostics.set(group.uri, group.items);
        projectUris.set(config, [...groups.values()].map(group => group.uri));
    }

    async function exportProject(config, automatic = false, check = false) {
        if (stopping || !vscode.workspace.isTrusted) return;
        if (active.has(config)) { if (automatic) active.get(config).pending = true; else vscode.window.showInformationMessage('TabForge 正在执行，请等待完成。'); return; }
        const root = path.dirname(config);
        const settings = vscode.workspace.getConfiguration('tabforge', vscode.Uri.file(config));
        const tool = toolPath(root, context.extensionPath, settings.get('toolPath', ''));
        if (!tool) {
            output.appendLine('找不到导出工具：安装便携版 VSIX，或将工具放入项目 Tools/TabForge。');
            if (!automatic) { output.show(true); vscode.window.showErrorMessage('TabForge 导出工具缺失，请查看输出面板。'); }
            return;
        }
        const state = { pending: false, child: undefined };
        active.set(config, state);
        if (!automatic) output.show(true);
        try {
            output.appendLine(`\n导出：${config}`);
            fs.rmSync(path.join(root, '.tabforge-report.json'), {force:true});
            const job = runExport(tool, config, text => output.append(text), {report:true, check});
            state.child = job.child;
            await job.completion;
            if (!automatic) vscode.window.showInformationMessage(check ? 'TabForge 校验通过' : 'TabForge 导出成功');
        } catch (error) {
            output.appendLine(String(error));
            output.show(true);
            vscode.window.showErrorMessage('TabForge 导出失败，请查看输出中的文件和位置。');
        } finally {
            updateDiagnostics(config);
            active.delete(config);
            if (state.pending && !stopping) void exportProject(config, true);
        }
    }

    async function selectedProject() {
        let config;
        const editor = vscode.window.activeTextEditor;
        if (editor?.document.uri.scheme === 'file') config = findProject(path.dirname(editor.document.uri.fsPath));
        if (!config) {
            const configs = [...new Set((vscode.workspace.workspaceFolders ?? []).map(folder => findProject(folder.uri.fsPath)).filter(Boolean))];
            config = configs.length === 1 ? configs[0] : await vscode.window.showQuickPick(configs, { placeHolder: '选择 TabForge 项目' });
        }
        if (!config) vscode.window.showErrorMessage('当前项目没有 tabforge.json。');
        return config;
    }
    for (const [command, check] of [['tabforge.export', false], ['tabforge.check', true]]) {
        context.subscriptions.push(vscode.commands.registerCommand(command, async () => { const config = await selectedProject(); if (config) await exportProject(config, false, check); }));
    }
    context.subscriptions.push(vscode.commands.registerCommand('tabforge.report', async () => {
        const config = await selectedProject(); if (!config) return;
        try { await vscode.window.showTextDocument(await vscode.workspace.openTextDocument(vscode.Uri.file(path.join(path.dirname(config), '.tabforge-report.json')))); }
        catch { vscode.window.showInformationMessage('先执行导出或校验，再查看报告。'); }
    }));

    // This also catches Excel saves performed outside the text editor.
    const watcher = vscode.workspace.createFileSystemWatcher('**/*.{proto,csv,xlsx,json}');
    const changed = uri => {
        if (uri.scheme !== 'file' || stopping) return;
        const config = findProject(path.dirname(uri.fsPath));
        if (!config || !vscode.workspace.getConfiguration('tabforge', vscode.Uri.file(config)).get('exportOnSave', false)) return;
        try {
            const project = JSON.parse(fs.readFileSync(config, 'utf8'));
            if (!isSourceFile(path.dirname(config), uri.fsPath, project)) return;
        } catch { /* Export will report malformed project configuration. */ }
        clearTimeout(timers.get(config));
        timers.set(config, setTimeout(() => { timers.delete(config); void exportProject(config, true); }, 600));
    };
    context.subscriptions.push(watcher, watcher.onDidChange(changed), watcher.onDidCreate(changed), watcher.onDidDelete(changed));
}

function deactivate() {
    stopping = true;
    for (const timer of timers.values()) clearTimeout(timer);
    timers.clear();
    for (const job of active.values()) job.child?.kill();
}

module.exports = { activate, deactivate };

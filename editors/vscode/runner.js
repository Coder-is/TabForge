const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');

function findProject(start) {
    let dir = path.resolve(start);
    for (;;) {
        const config = path.join(dir, 'tabforge.json');
        if (fs.existsSync(config)) return config;
        const parent = path.dirname(dir);
        if (parent === dir) return undefined;
        dir = parent;
    }
}

function toolPath(root, extensionPath, override = '', platform = process.platform, arch = process.arch) {
    const filename = platform === 'win32' ? 'tabforge.exe' : 'tabforge';
    const choices = [override, path.join(root, 'Tools', 'TabForge', filename), path.join(extensionPath, 'bin', `${platform}-${arch}`, filename)];
    return choices.find(candidate => candidate && path.isAbsolute(candidate) && fs.existsSync(candidate));
}

function runExport(tool, config, onOutput) {
    const child = spawn(tool, ['-project=' + config], { cwd: path.dirname(config), shell: false, windowsHide: true });
    child.stdout.on('data', data => onOutput(data.toString()));
    child.stderr.on('data', data => onOutput(data.toString()));
    const completion = new Promise((resolve, reject) => {
        child.once('error', reject);
        child.once('close', (code, signal) => code === 0 ? resolve() : reject(new Error(`Export exited with ${code ?? signal}`)));
    });
    return { child, completion };
}

function isSourceFile(root, file, config) {
    if (path.basename(file).startsWith('~$')) return false;
    const relative = path.relative(root, file);
    const parts = relative.split(path.sep);
    if (parts.some(part => part.startsWith('.tabforge-'))) return false;
    const inside = name => {
        const candidate = path.resolve(root, name);
        const rel = path.relative(candidate, file);
        return rel === '' || (!rel.startsWith('..' + path.sep) && rel !== '..' && !path.isAbsolute(rel));
    };
    const generated = config.output || 'Generated';
    if (inside(generated) || inside(generated + '.tabforge-backup')) return false;
    if (file === path.join(root, 'tabforge.json')) return true;
    const sources = [];
    if (config.schema) sources.push(config.schema.dir || 'Protocols', ...(config.schema.imports || []));
    if (config.protocol) sources.push(config.protocol);
    for (const table of config.tables || []) {
        if (table.index) sources.push(path.dirname(table.index));
        if (table.mapping) sources.push(table.mapping);
    }
    return sources.some(inside);
}

module.exports = { findProject, toolPath, runExport, isSourceFile };

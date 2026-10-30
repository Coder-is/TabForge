const fs = require('node:fs');
const path = require('node:path');

// Report columns follow their producer: protocompile counts runes with 8-column
// tab stops and Go's csv.Reader counts UTF-8 bytes. VS Code ranges use UTF-16.
function editorColumn(text, line, column, bytes) {
  let width = 0, units = 0;
  for (const char of text) {
    if (width >= column) break;
    width += bytes ? Buffer.byteLength(char) : char === '\t' ? 8 - (width % 8) : 1;
    units += char.length;
  }
  // csv.Reader counts a UTF-8 BOM, while VS Code hides it from the first line.
  return bytes && line === 0 && text.startsWith('\uFEFF') ? Math.max(0, units - 1) : units;
}

function sourceColumn(file, line, column, linesOf) {
  const extension = path.extname(file).toLowerCase();
  if (column === 0 || (extension !== '.proto' && extension !== '.csv')) return column;
  try {
    const text = linesOf(file)[line];
    return text === undefined ? column : editorColumn(text, line, column, extension === '.csv');
  } catch { return column; }
}

function diagnosticEntries(report, config, readText = file => fs.readFileSync(file, 'utf8')) {
  if (report?.format !== 'tabforge.report.v1' || !Array.isArray(report.diagnostics)) return [];
  const cache = new Map();
  const linesOf = file => { if (!cache.has(file)) cache.set(file, readText(file).split(/\r?\n/)); return cache.get(file); };
  return report.diagnostics.map(item => {
    const workbook = /\.(xlsx|xlsm|xls)$/i.test(item.path || '');
    const file = workbook ? config : item.path || config;
    const line = workbook ? 0 : Math.max(0, (item.line || 1) - 1);
    return {
      path: file,
      line,
      column: workbook ? 0 : sourceColumn(file, line, Math.max(0, (item.column || 1) - 1), linesOf),
      code: item.code,
      message: [workbook ? `${path.basename(item.path)} ${item.sheet || ''} ${item.cell || ''}` : '', item.message, item.hint].filter(Boolean).join('\n')
    };
  });
}
module.exports = { diagnosticEntries };

const path = require('node:path');

function diagnosticEntries(report, config) {
  if (report?.format !== 'tabforge.report.v1' || !Array.isArray(report.diagnostics)) return [];
  return report.diagnostics.map(item => {
    const workbook = /\.(xlsx|xlsm|xls)$/i.test(item.path || '');
    return {
      path: workbook ? config : item.path || config,
      line: workbook ? 0 : Math.max(0, (item.line || 1) - 1),
      column: workbook ? 0 : Math.max(0, (item.column || 1) - 1),
      code: item.code,
      message: [workbook ? `${path.basename(item.path)} ${item.sheet || ''} ${item.cell || ''}` : '', item.message, item.hint].filter(Boolean).join('\n')
    };
  });
}
module.exports = { diagnosticEntries };

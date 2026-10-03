const sdk = require('../../vendor/tabforge.js');
Page({
  data: { baseURL: 'http://127.0.0.1:18083', running: false, done: false, results: [], output: '等待运行' },
  async run() {
    this.setData({ running: true, done: false, output: 'Running…' });
    try {
      const results = await sdk.test(wx, this.data.baseURL);
      this.setData({ results, output: JSON.stringify(results, null, 2), done: true });
    } catch (error) { this.setData({ output: String(error.stack || error), done: true, results: [{ passed: false, message: String(error) }] }); }
    this.setData({ running: false });
    return this.data.results;
  }
});

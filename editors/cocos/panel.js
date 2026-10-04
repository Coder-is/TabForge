module.exports = Editor.Panel.define({
  template: `<div id="actions"><button id="init">创建示例</button><button id="check">校验</button><button id="export">导出并导入</button><button id="import">导入已有包</button><button id="cancel">取消</button></div><pre id="log"></pre>`,
  style: `#actions { display:flex; flex-wrap:wrap; gap:8px; padding:12px; } #log { white-space:pre-wrap; overflow-wrap:anywhere; padding:12px; user-select:text; }`,
  $: {init:'#init',check:'#check',export:'#export',import:'#import',cancel:'#cancel',log:'#log'},
  async ready() {
    const call = async action => {
      try { await Editor.Message.request('tabforge', action); }
      catch (error) { this.$.log.textContent = String(error); }
    };
    for (const action of ['init','check','export','import','cancel']) this.$[action].addEventListener('click', () => void call(action));
    let updating = false;
    const update = async () => {
      if (updating) return;
      updating = true;
      try {
        const status = await Editor.Message.request('tabforge', 'query-status');
        this.$.log.textContent = status.log;
        for (const action of ['init','check','export','import']) this.$[action].disabled = status.running;
        this.$.cancel.disabled = !status.running;
      } catch { /* A disabled extension no longer answers. */ }
      finally { updating = false; }
    };
    this.timer = setInterval(update, 400);
    await update();
  },
  close() { clearInterval(this.timer); }
});

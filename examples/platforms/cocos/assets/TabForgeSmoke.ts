import { _decorator, Component } from 'cc';
import { CocosTransport, WechatTransport, protocolVersion, schemaHash, wireSchema, runSmoke } from './tabforge.js';
const { ccclass, property } = _decorator;
@ccclass('TabForgeSmoke')
export class TabForgeSmoke extends Component {
  @property baseURL = 'http://127.0.0.1:18083';
  @property backend: 'fetch' | 'xhr' | 'wechat' = 'fetch';
  async start() {
    const options = { schemaHash, wireSchema };
    const api = (globalThis as any).wx;
    const transport = this.backend === 'wechat'
      ? new WechatTransport(this.baseURL, protocolVersion, api, options)
      : new CocosTransport(this.baseURL, protocolVersion, { ...options, backend: this.backend });
    console.log('TABFORGE_PLATFORM_REPORT', JSON.stringify(await runSmoke(transport)));
  }
}

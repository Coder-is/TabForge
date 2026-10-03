import { WechatTransport } from "../../sdk/wechat/transport.ts";
import type { WxAPI } from "../../sdk/wechat/transport.ts";
import { protocolVersion, schemaHash, wireSchema } from "../protocol/generated/types.ts";
import { runSmoke } from "./smoke.ts";
export function test(api: WxAPI, baseURL: string) {
  return runSmoke(new WechatTransport(baseURL, protocolVersion, api, { schemaHash, wireSchema }));
}

/**
 * JSON budgets shared by every TabForge runtime. Depth counts container nesting
 * (the root value has depth 0, members of a root object depth 1); nodes count
 * every JSON value, scalars and containers alike, but not object keys.
 * Network messages are also bounded by the 1 MiB transport limits. Data bundle
 * files are not, so a 20,000-row table needs the larger data budget.
 */
export interface JSONLimits {
    readonly maxDepth: number;
    readonly maxNodes: number;
}
export const PROTOCOL_LIMITS: JSONLimits = Object.freeze({ maxDepth: 64, maxNodes: 100000 });
export const DATA_LIMITS: JSONLimits = Object.freeze({ maxDepth: 64, maxNodes: 10000000 });

/**
 * Exact source text of numbers whose JavaScript value does not print back to
 * the same text, e.g. 1.0, 1e2 or 18446744073709551615. ProtoJSON integer rules
 * are decided on that text. The root value is stored under JSON_ROOT.
 */
export type NumberSources = WeakMap<object, Map<string, string>>;
export const JSON_ROOT: object = Object.freeze({});

const NUMBER = /-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/y;

/** Parse strict JSON, rejecting duplicate members (also after unescaping) and oversized input. */
export function parseJSON(text: string, limits: JSONLimits = PROTOCOL_LIMITS, sources?: NumberSources): unknown {
    let pos = 0, nodes = 0;
    const fail = (): never => { throw new Error(`Invalid JSON at offset ${pos}`); };
    const space = (): void => {
        for (;;) {
            const code = text.charCodeAt(pos);
            if (code !== 32 && code !== 9 && code !== 10 && code !== 13) return;
            pos++;
        }
    };
    const string = (): string => {
        const start = pos++;
        let escaped = false;
        while (pos < text.length) {
            const code = text.charCodeAt(pos++);
            if (code === 34) return escaped ? JSON.parse(text.slice(start, pos)) as string : text.slice(start + 1, pos - 1);
            if (code < 32) fail();
            if (code === 92) { escaped = true; pos++; }
        }
        return fail();
    };
    const record = (holder: object, key: string, raw: string, value: number): void => {
        if (!sources || String(value) === raw) return;
        let map = sources.get(holder);
        if (!map) sources.set(holder, map = new Map());
        map.set(key, raw);
    };
    const value = (depth: number, holder: object, key: string): unknown => {
        space();
        if (depth > limits.maxDepth || ++nodes > limits.maxNodes) throw new Error(`JSON exceeds depth ${limits.maxDepth} or ${limits.maxNodes} values`);
        const code = text.charCodeAt(pos);
        if (code === 34) return string();
        if (code === 123) {
            pos++; space();
            const result: Record<string, unknown> = {};
            if (text.charCodeAt(pos) === 125) { pos++; return result; }
            for (;;) {
                if (text.charCodeAt(pos) !== 34) fail();
                const name = string();
                if (Object.prototype.hasOwnProperty.call(result, name)) throw new Error(`Duplicate JSON member ${name}`);
                space(); if (text.charCodeAt(pos++) !== 58) fail();
                const item = value(depth + 1, result, name);
                // Define, never assign: "__proto__" must stay an ordinary member.
                if (name === "__proto__") Object.defineProperty(result, name, { value: item, writable: true, enumerable: true, configurable: true });
                else result[name] = item;
                space();
                const next = text.charCodeAt(pos++);
                if (next === 125) return result;
                if (next !== 44) fail();
                space();
            }
        }
        if (code === 91) {
            pos++; space();
            const result: unknown[] = [];
            if (text.charCodeAt(pos) === 93) { pos++; return result; }
            for (;;) {
                result.push(value(depth + 1, result, String(result.length)));
                space();
                const next = text.charCodeAt(pos++);
                if (next === 93) return result;
                if (next !== 44) fail();
            }
        }
        for (const [word, literal] of [["true", true], ["false", false], ["null", null]] as const) {
            if (text.startsWith(word, pos)) { pos += word.length; return literal; }
        }
        NUMBER.lastIndex = pos;
        const match = NUMBER.exec(text);
        if (!match) return fail();
        pos += match[0].length;
        const number = Number(match[0]);
        record(holder, key, match[0], number);
        return number;
    };
    const result = value(0, JSON_ROOT, "");
    space(); if (pos !== text.length) fail();
    return result;
}

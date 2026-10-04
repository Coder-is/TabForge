/** Parse strict JSON without losing duplicate members before validation. */
export function parseJSON(text: string): unknown {
    let pos = 0, nodes = 0;
    const fail = (): never => { throw new Error(`Invalid JSON at offset ${pos}`); };
    const space = (): void => { while (/^[ \t\r\n]$/.test(text[pos] ?? "")) pos++; };
    const string = (): string => {
        const start = pos++;
        while (pos < text.length) {
            const char = text[pos++];
            if (char === '"') return JSON.parse(text.slice(start, pos)) as string;
            if (char === "\\") pos++;
        }
        return fail();
    };
    const value = (depth: number): void => {
        space(); if (depth > 64 || ++nodes > 100000) fail();
        const char = text[pos];
        if (char === '"') { string(); return; }
        if (char === "{" || char === "[") {
            pos++; space(); const end = char === "{" ? "}" : "]", keys = new Set<string>();
            if (text[pos] === end) { pos++; return; }
            for (;;) {
                if (char === "{") {
                    if (text[pos] !== '"') fail();
                    const key = string(); if (keys.has(key)) throw new Error(`Duplicate JSON member ${key}`);
                    keys.add(key); space(); if (text[pos++] !== ":") fail();
                }
                value(depth + 1); space();
                const next = text[pos++]; if (next === end) return;
                if (next !== ",") fail(); space();
            }
        }
        const match = /^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/.exec(text.slice(pos));
        if (!match) fail(); else pos += match[0].length;
    };
    value(0); space(); if (pos !== text.length) fail();
    return JSON.parse(text) as unknown;
}

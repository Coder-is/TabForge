// Browser WebSocket bridge for TabForge.Protocol.WebGLWebSocketTransport.
// Unity WebGL builds have no System.Net.WebSockets; the C# side opens sockets
// through these functions and receives events through the callbacks it
// registers with TabForgeWS_Init. Every event is delivered asynchronously
// (never from inside a C# call), and close is delivered exactly once.
var TabForgeWebSocketLibrary = {
    $TabForgeWS: {
        sockets: {},
        next: 1,
        onOpen: 0,
        onMessage: 0,
        onClose: 0,
        // Copies bytes into the wasm heap for the duration of one callback.
        deliver: function (id, bytes, text) {
            var ptr = _malloc(bytes.length || 1);
            HEAPU8.set(bytes, ptr);
            try {
                {{{ makeDynCall('viiii', 'TabForgeWS.onMessage') }}}(id, ptr, bytes.length, text ? 1 : 0);
            } finally {
                _free(ptr);
            }
        },
        finish: function (id) {
            var entry = TabForgeWS.sockets[id];
            if (!entry)
                return;
            delete TabForgeWS.sockets[id];
            if (entry.timer)
                clearTimeout(entry.timer);
            var ws = entry.ws;
            if (ws) {
                ws.onopen = ws.onmessage = ws.onerror = ws.onclose = null;
                try { ws.close(); } catch (e) { /* already closing */ }
            }
            {{{ makeDynCall('vi', 'TabForgeWS.onClose') }}}(id);
        }
    },

    TabForgeWS_Init__deps: ['$TabForgeWS'],
    TabForgeWS_Init: function (onOpen, onMessage, onClose) {
        TabForgeWS.onOpen = onOpen;
        TabForgeWS.onMessage = onMessage;
        TabForgeWS.onClose = onClose;
    },

    // Returns a socket id (> 0). Failures, including an invalid URL, arrive
    // later as the close callback.
    TabForgeWS_Open__deps: ['$TabForgeWS'],
    TabForgeWS_Open: function (urlPtr, protocolPtr, maxMessageBytes) {
        var id = TabForgeWS.next++;
        var entry = { ws: null, timer: 0, closing: false };
        TabForgeWS.sockets[id] = entry;
        var url = UTF8ToString(urlPtr);
        var protocol = UTF8ToString(protocolPtr);
        var ws;
        try {
            ws = new WebSocket(url, [protocol]);
        } catch (e) {
            entry.timer = setTimeout(function () { TabForgeWS.finish(id); }, 0);
            return id;
        }
        entry.ws = ws;
        ws.binaryType = 'arraybuffer';
        ws.onopen = function () {
            var accepted = ws.protocol || '';
            var size = lengthBytesUTF8(accepted) + 1;
            var ptr = _malloc(size);
            stringToUTF8(accepted, ptr, size);
            try {
                {{{ makeDynCall('vii', 'TabForgeWS.onOpen') }}}(id, ptr);
            } finally {
                _free(ptr);
            }
        };
        ws.onmessage = function (event) {
            if (entry.closing)
                return;
            var text = typeof event.data === 'string';
            var bytes = text ? new TextEncoder().encode(event.data) : new Uint8Array(event.data);
            if (bytes.length > maxMessageBytes) {
                TabForgeWS.finish(id);
                return;
            }
            TabForgeWS.deliver(id, bytes, text);
        };
        ws.onerror = function () { TabForgeWS.finish(id); };
        ws.onclose = function () { TabForgeWS.finish(id); };
        return id;
    },

    // Returns 1 if the message was queued, 0 if the socket is not open.
    TabForgeWS_Send__deps: ['$TabForgeWS'],
    TabForgeWS_Send: function (id, dataPtr, length, text) {
        var entry = TabForgeWS.sockets[id];
        if (!entry || entry.closing || !entry.ws || entry.ws.readyState !== 1)
            return 0;
        // Copy out of the heap: it may be reused or grow before the send runs.
        var bytes = HEAPU8.slice(dataPtr, dataPtr + length);
        try {
            entry.ws.send(text ? new TextDecoder().decode(bytes) : bytes.buffer);
        } catch (e) {
            return 0;
        }
        return 1;
    },

    // Sends what is already queued, then closes. The close callback follows
    // when the browser finishes, or after one second at the latest.
    TabForgeWS_Close__deps: ['$TabForgeWS'],
    TabForgeWS_Close: function (id) {
        var entry = TabForgeWS.sockets[id];
        if (!entry || entry.closing)
            return;
        entry.closing = true;
        if (entry.timer)
            clearTimeout(entry.timer);
        entry.timer = setTimeout(function () { TabForgeWS.finish(id); }, entry.ws ? 1000 : 0);
        if (entry.ws) {
            try { entry.ws.close(1000); } catch (e) { /* already closing */ }
        }
    }
};

mergeInto(LibraryManager.library, TabForgeWebSocketLibrary);

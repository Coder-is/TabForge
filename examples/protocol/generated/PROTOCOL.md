# TabForge chat example

Protocol version: `1.0.0`; contract format: `1`.

Generated from Proto + manifest. All endpoints use POST with `Content-Type: application/json` and ProtoJSON request bodies. A version mismatch returns HTTP 409.

Headers: `X-Protocol-Version` (required), `X-Request-ID` (optional; server creates one if absent), and `Authorization: Bearer …` when declared. Authentication must be implemented by the application.

ProtoJSON uses lowerCamelCase (or explicit json_name), decimal strings for 64-bit integers, base64 bytes, enum names, omitted defaults, and at most one member per oneof. Typescript types describe the emitted wire shape, not binary Protobuf objects.

## chatComplete

- Route: `POST /v1/chat/complete`
- Transport: `http_json`
- Auth: `none`
- Timeout: 30000 ms (whole request/stream)
- RPC: `tabforge.example.ChatService.Complete`
- Request: `tabforge.example.ChatRequest`
- Response: `tabforge.example.ChatResponse`

Success: `{"protocolVersion":"…","requestId":"…","data":{…}}`. Failures use a non-2xx status and an `error` member instead of `data`.

## chatStream

- Route: `POST /v1/chat/stream`
- Transport: `http_sse`
- Auth: `none`
- Timeout: 120000 ms (whole request/stream)
- RPC: `tabforge.example.ChatService.Stream`
- Request: `tabforge.example.ChatRequest`
- Response: `tabforge.example.ChatEvent`

Response: `text/event-stream`. Each SSE `data` is `{"protocolVersion":"…","requestId":"…","sequence":"1","payload":{…}}`. Sequence starts at 1 and increments by 1; SSE `id` is the same decimal string. `payload` is the full response oneof message, not only its nested member.

| SSE event | Proto field | Payload type | Terminal |
| --- | --- | --- | --- |
| text.delta | delta | tabforge.example.TextDelta | false |
| tool.delta | toolDelta | tabforge.example.ToolCallDelta | false |
| usage | usage | tabforge.example.Usage | false |
| completed | completed | tabforge.example.ChatCompleted | true |
| failed | failed | tabforge.example.ChatFailed | true |

A terminal event ends the stream. An EOF before a terminal event is a failure. Transport failures after headers use the reserved terminal `protocol.error` event with `error` instead of `payload`. Cancelling the client aborts the request; it does not imply provider-side work was cancelled. No automatic retries or replay are promised in v1.

## Errors

`error` has `{ code: string, message: string, retryable: boolean }`. Business errors belong in declared Proto messages/events. Transport codes: `bad_request`, `not_found`, `method_not_allowed`, `unsupported_media_type`, `unauthorized`, `version_mismatch`, `timeout`, `internal`, `incomplete_stream`.

## Message types

Fields may be omitted according to ProtoJSON presence/default rules. `oneof` members are mutually exclusive.

### tabforge.example.ChatCompleted

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| response | tabforge.example.ChatResponse | T_tabforge__example__ChatResponse | explicit presence |  |

### tabforge.example.ChatEvent

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| delta | tabforge.example.TextDelta | T_tabforge__example__TextDelta | oneof payload |  |
| toolDelta | tabforge.example.ToolCallDelta | T_tabforge__example__ToolCallDelta | oneof payload |  |
| usage | tabforge.example.Usage | T_tabforge__example__Usage | oneof payload |  |
| completed | tabforge.example.ChatCompleted | T_tabforge__example__ChatCompleted | oneof payload |  |
| failed | tabforge.example.ChatFailed | T_tabforge__example__ChatFailed | oneof payload |  |

### tabforge.example.ChatFailed

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| code | string | string | default may be omitted |  |
| message | string | string | default may be omitted |  |
| retryable | bool | boolean | default may be omitted |  |

### tabforge.example.ChatRequest

Application-owned types. No dependency on any model vendor's event schema.

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| prompt | string | string | default may be omitted |  |
| conversationId | uint64 | string | default may be omitted |  |

### tabforge.example.ChatResponse

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| text | string | string | default may be omitted |  |
| usage | tabforge.example.Usage | T_tabforge__example__Usage | explicit presence |  |

### tabforge.example.TextDelta

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| text | string | string | default may be omitted |  |

### tabforge.example.ToolCallDelta

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| callId | string | string | default may be omitted |  |
| name | string | string | default may be omitted |  |
| argumentsDelta | string | string | default may be omitted | Fragments can be incomplete JSON. Assemble by call_id before parsing. |

### tabforge.example.Usage

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| inputTokens | uint64 | string | default may be omitted |  |
| outputTokens | uint64 | string | default may be omitted |  |

Wire mapping: [official ProtoJSON specification](https://protobuf.dev/programming-guides/json/). Framing: [WHATWG SSE specification](https://html.spec.whatwg.org/multipage/server-sent-events.html).

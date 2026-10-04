# TabForge complete example

Protocol version: `1.0.0`; contract format: `1`.

Schema hash: `c3de9d4da787ab47e37a24744677c5dae0d5c8da0533b8bd22b57cc15cf0ecdd`.

Generated from Proto + manifest. All endpoints use POST with `Content-Type: application/json` and ProtoJSON request bodies. A version mismatch returns HTTP 409.

Headers: `X-Protocol-Version` and `X-Protocol-Schema` (required by default), `X-Request-ID` (optional; server creates one if absent), and `Authorization: Bearer …` when declared. Authentication must be implemented by the application. Envelope responses also include `schemaHash`. The hash is contract identity, not authentication. Generated `wireSchema` validates ProtoJSON fields in all supplied SDKs; `runtime.json` supplies contract metadata to Unity and Unreal.

ProtoJSON uses lowerCamelCase (or explicit json_name), decimal strings for 64-bit integers, base64 bytes, enum names, omitted defaults, and at most one member per oneof. Typescript types describe the emitted wire shape, not binary Protobuf objects.

## chatComplete

- Route: `POST /v1/chat/complete`
- Transport: `http_json`
- Auth: `none`
- Timeout: 30000 ms (whole request/stream)
- RPC: `tabforge.demo.chat.ChatService.Complete`
- Request: `tabforge.demo.chat.ChatRequest`
- Response: `tabforge.demo.chat.ChatResponse`

Success: `{"protocolVersion":"…","schemaHash":"…","requestId":"…","data":{…}}`. Failures use a non-2xx status and an `error` member instead of `data`.

## chatStream

- Route: `POST /v1/chat/stream`
- Transport: `http_sse`
- Auth: `none`
- Timeout: 30000 ms (whole request/stream)
- RPC: `tabforge.demo.chat.ChatService.Stream`
- Request: `tabforge.demo.chat.ChatRequest`
- Response: `tabforge.demo.chat.ChatEvent`

Response: `text/event-stream`. Each SSE `data` is `{"protocolVersion":"…","schemaHash":"…","requestId":"…","sequence":"1","payload":{…}}`. Sequence starts at 1 and increments by 1; SSE `id` is the same decimal string. `payload` is the full response oneof message, not only its nested member.

| SSE event | Proto field | Payload type | Terminal |
| --- | --- | --- | --- |
| text.delta | delta | tabforge.demo.chat.TextDelta | false |
| completed | completed | tabforge.demo.chat.Completed | true |
| failed | failed | tabforge.demo.chat.Failed | true |

A terminal event ends the stream. An EOF before a terminal event is a failure. Transport failures after headers use the reserved terminal `protocol.error` event with `error` instead of `payload`. Cancelling the client aborts the request; it does not imply provider-side work was cancelled. No automatic retries or replay are promised in v1.

## Errors

`error` has `{ code: string, message: string, retryable: boolean }`. Business errors belong in declared Proto messages/events. Transport codes: `bad_request`, `request_too_large`, `not_found`, `method_not_allowed`, `unsupported_media_type`, `unauthorized`, `version_mismatch`, `schema_mismatch`, `timeout`, `internal`, `incomplete_stream`. Limits default to 1 MiB per request/response/frame. SSE comments provide keepalive without consuming event sequence. Go handlers must respect context cancellation. Deploy using `httptransport.HTTPServer` and an explicit CORS origin allowlist where needed.

## Message types

Fields may be omitted according to ProtoJSON presence/default rules. `oneof` members are mutually exclusive.

### tabforge.demo.chat.ChatEvent

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| delta | tabforge.demo.chat.TextDelta | T_tabforge__demo__chat__TextDelta | oneof payload |  |
| completed | tabforge.demo.chat.Completed | T_tabforge__demo__chat__Completed | oneof payload |  |
| failed | tabforge.demo.chat.Failed | T_tabforge__demo__chat__Failed | oneof payload |  |

### tabforge.demo.chat.ChatRequest

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| prompt | string | string | default may be omitted |  |
| conversationId | uint64 | string | default may be omitted |  |

### tabforge.demo.chat.ChatResponse

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| text | string | string | default may be omitted |  |

### tabforge.demo.chat.Completed

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| response | tabforge.demo.chat.ChatResponse | T_tabforge__demo__chat__ChatResponse | explicit presence |  |

### tabforge.demo.chat.Failed

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| message | string | string | default may be omitted |  |

### tabforge.demo.chat.TextDelta

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| text | string | string | default may be omitted |  |

Wire mapping: [official ProtoJSON specification](https://protobuf.dev/programming-guides/json/). Framing: [WHATWG SSE specification](https://html.spec.whatwg.org/multipage/server-sent-events.html).

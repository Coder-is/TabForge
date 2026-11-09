# TabForge realtime game example

Protocol version: `1.0.0`; contract format: `1`.

Schema hash: `444f00749685fe08c4406fa307653d23f718f323fbeffbd45a0e348ca7b9e862`.

Generated from Proto + manifest. All endpoints use POST with `Content-Type: application/json` and ProtoJSON request bodies. A version mismatch returns HTTP 409.

Headers: `X-Protocol-Version` and `X-Protocol-Schema` (required by default), `X-Request-ID` (optional; server creates one if absent), and `Authorization: Bearer …` when declared. Authentication must be implemented by the application. Envelope responses also include `schemaHash`. The hash is contract identity, not authentication. Generated `wireSchema` validates ProtoJSON fields in all supplied SDKs; `runtime.json` supplies contract metadata to Unity and Unreal.

ProtoJSON uses lowerCamelCase (or explicit json_name), decimal strings for 64-bit integers, base64 bytes, enum names, omitted defaults, and at most one member per oneof. Typescript types describe the emitted wire shape, not binary Protobuf objects.

## Realtime channels

Channels are WebSocket connections. The client offers subprotocol `tabforge.realtime.v1.proto` (binary frames, Protobuf payloads) or `tabforge.realtime.v1.json` (text frames, ProtoJSON payloads). The first frame is `hello` (protocol version, schema hash, bearer token, or session id + resume token); the server answers `welcome` or `close`. Request, response, push and notify frames are numbered per direction and acknowledged; after a reconnect the session resumes and unacknowledged frames are retransmitted, so each is delivered exactly once and in order while the session lives. Volatile messages are not numbered or retransmitted. The client sends `ping` every heartbeat interval. Frame layout and recovery rules: [realtime protocol](https://github.com/Coder-is/TabForge/blob/main/doc/realtime.md).

### Channel game

- Path: `/ws/game` (WebSocket)
- Auth: `bearer`
- Heartbeat: 5000 ms
- Resume window: 60000 ms

| ID | Name | Kind | Payload | Response | Timeout | Delivery |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | login | request | tabforge.game.LoginRequest | tabforge.game.LoginReply | 5000 ms | reliable |
| 2 | joinRoom | request | tabforge.game.JoinRoomRequest | tabforge.game.RoomState | 5000 ms | reliable |
| 3 | chat | request | tabforge.game.ChatSend | tabforge.game.ChatAck | 5000 ms | reliable |
| 4 | buy | request | tabforge.game.BuyRequest | tabforge.game.BuyReply | 10000 ms | reliable |
| 100 | move | notify | tabforge.game.Move |  |  | volatile |
| 200 | playerMoved | push | tabforge.game.PlayerMoved |  |  | volatile |
| 201 | chatMessage | push | tabforge.game.ChatMessage |  |  | reliable |
| 202 | notice | push | tabforge.game.ServerNotice |  |  | reliable |

## Errors

`error` has `{ code: string, message: string, retryable: boolean }`. Business errors belong in declared Proto messages/events. Transport codes: `bad_request`, `request_too_large`, `not_found`, `method_not_allowed`, `unsupported_media_type`, `unauthorized`, `version_mismatch`, `schema_mismatch`, `timeout`, `internal`, `incomplete_stream`. Limits default to 1 MiB per request/response/frame. SSE comments provide keepalive without consuming event sequence. Go handlers must respect context cancellation. Deploy using `httptransport.HTTPServer` and an explicit CORS origin allowlist where needed.

## Message types

Fields may be omitted according to ProtoJSON presence/default rules. `oneof` members are mutually exclusive.

### google.protobuf.Timestamp

Well-known ProtoJSON representation: `string`.

### tabforge.game.BuyReply

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| gold | int64 | string | default may be omitted |  |
| inventory | map<uint32, uint32> | { [key: string]: number } | empty may be omitted |  |

### tabforge.game.BuyRequest

Purchases must run exactly once even when the network drops mid-request.

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| itemId | uint32 | number | default may be omitted |  |
| count | uint32 | number | default may be omitted |  |

### tabforge.game.ChatAck

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| messageId | uint64 | string | default may be omitted |  |

### tabforge.game.ChatMessage

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| messageId | uint64 | string | default may be omitted |  |
| from | uint64 | string | default may be omitted |  |
| text | string | string | default may be omitted |  |
| sentAt | google.protobuf.Timestamp | T_google__protobuf__Timestamp | explicit presence |  |

### tabforge.game.ChatSend

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| text | string | string | default may be omitted |  |

### tabforge.game.JoinRoomRequest

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| room | string | string | default may be omitted |  |

### tabforge.game.LoginReply

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| playerId | uint64 | string | default may be omitted |  |
| nickname | string | string | default may be omitted |  |
| gold | int64 | string | default may be omitted |  |

### tabforge.game.LoginRequest

Sent once after connecting; the bearer token travels in the hello frame.

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| nickname | string | string | default may be omitted |  |

### tabforge.game.Move

Client to server, no reply. Sent many times per second.

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| position | tabforge.game.Vec2 | T_tabforge__game__Vec2 | explicit presence |  |
| tick | uint32 | number | default may be omitted |  |

### tabforge.game.Player

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| playerId | uint64 | string | default may be omitted |  |
| nickname | string | string | default may be omitted |  |
| position | tabforge.game.Vec2 | T_tabforge__game__Vec2 | explicit presence |  |

### tabforge.game.PlayerMoved

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| playerId | uint64 | string | default may be omitted |  |
| position | tabforge.game.Vec2 | T_tabforge__game__Vec2 | explicit presence |  |
| tick | uint32 | number | default may be omitted |  |

### tabforge.game.RoomState

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| room | string | string | default may be omitted |  |
| players | repeated tabforge.game.Player | Array<T_tabforge__game__Player> | empty may be omitted |  |

### tabforge.game.ServerNotice

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| kind | tabforge.game.Notice | T_tabforge__game__Notice | default may be omitted |  |
| text | string | string | default may be omitted |  |
| minutesLeft | uint32 | number | oneof detail |  |
| eventName | string | string | oneof detail |  |

### tabforge.game.Vec2

| JSON field | Proto type | JSON / TS wire type | Presence | Description |
| --- | --- | --- | --- | --- |
| x | float | number \| "NaN" \| "Infinity" \| "-Infinity" | default may be omitted |  |
| y | float | number \| "NaN" \| "Infinity" \| "-Infinity" | default may be omitted |  |

### tabforge.game.Notice

| Name | Number |
| --- | --- |
| NOTICE_UNSPECIFIED | 0 |
| NOTICE_MAINTENANCE | 1 |
| NOTICE_EVENT | 2 |

Wire mapping: [official ProtoJSON specification](https://protobuf.dev/programming-guides/json/). Framing: [WHATWG SSE specification](https://html.spec.whatwg.org/multipage/server-sent-events.html).

# Data schema

Schema hash: `2e48913f37aa76c168a4c2cc393ecad41cae2ef79f3ab584315f30eb8fd80aaa`. No RPC or transport is required.

Types describe ProtoJSON: decimal strings for int64/uint64, Base64 bytes, enum names, and omitted defaults. oneof members are mutually exclusive.

## google.protobuf.Timestamp



ProtoJSON: `string`.

## tabforge.game.BuyReply



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| gold | 1 | int64 | default may be omitted |  |
| inventory | 2 | map<uint32, uint32> | default may be omitted |  |

## tabforge.game.BuyRequest

Purchases must run exactly once even when the network drops mid-request.

| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| itemId | 1 | uint32 | default may be omitted |  |
| count | 2 | uint32 | default may be omitted |  |

## tabforge.game.ChatAck



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| messageId | 1 | uint64 | default may be omitted |  |

## tabforge.game.ChatMessage



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| messageId | 1 | uint64 | default may be omitted |  |
| from | 2 | uint64 | default may be omitted |  |
| text | 3 | string | default may be omitted |  |
| sentAt | 4 | google.protobuf.Timestamp | explicit presence |  |

## tabforge.game.ChatSend



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| text | 1 | string | default may be omitted |  |

## tabforge.game.JoinRoomRequest



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| room | 1 | string | default may be omitted |  |

## tabforge.game.LoginReply



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| playerId | 1 | uint64 | default may be omitted |  |
| nickname | 2 | string | default may be omitted |  |
| gold | 3 | int64 | default may be omitted |  |

## tabforge.game.LoginRequest

Sent once after connecting; the bearer token travels in the hello frame.

| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| nickname | 1 | string | default may be omitted |  |

## tabforge.game.Move

Client to server, no reply. Sent many times per second.

| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| position | 1 | tabforge.game.Vec2 | explicit presence |  |
| tick | 2 | uint32 | default may be omitted |  |

## tabforge.game.Player



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| playerId | 1 | uint64 | default may be omitted |  |
| nickname | 2 | string | default may be omitted |  |
| position | 3 | tabforge.game.Vec2 | explicit presence |  |

## tabforge.game.PlayerMoved



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| playerId | 1 | uint64 | default may be omitted |  |
| position | 2 | tabforge.game.Vec2 | explicit presence |  |
| tick | 3 | uint32 | default may be omitted |  |

## tabforge.game.RoomState



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| room | 1 | string | default may be omitted |  |
| players | 2 | repeated tabforge.game.Player | default may be omitted |  |

## tabforge.game.ServerNotice



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| kind | 1 | tabforge.game.Notice | default may be omitted |  |
| text | 2 | string | default may be omitted |  |
| minutesLeft | 3 | uint32 | oneof detail |  |
| eventName | 4 | string | oneof detail |  |

## tabforge.game.Vec2



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| x | 1 | float | default may be omitted |  |
| y | 2 | float | default may be omitted |  |

## tabforge.game.Notice

| Name | Number |
| --- | --- |
| NOTICE_UNSPECIFIED | 0 |
| NOTICE_MAINTENANCE | 1 |
| NOTICE_EVENT | 2 |


# Data schema

Schema hash: `a5c415d8c190f0b49e2779c69e83c7fc13f377308f362525b0258a8448b3a25a`. No RPC or transport is required.

Types describe ProtoJSON: decimal strings for int64/uint64, Base64 bytes, enum names, and omitted defaults. oneof members are mutually exclusive.

## google.protobuf.Any



ProtoJSON: `{ "@type": string; [key: string]: JsonValue }`.

## google.protobuf.BoolValue



ProtoJSON: `boolean`.

## google.protobuf.BytesValue



ProtoJSON: `string`.

## google.protobuf.DoubleValue



ProtoJSON: `number | "NaN" | "Infinity" | "-Infinity"`.

## google.protobuf.Duration



ProtoJSON: `string`.

## google.protobuf.FieldMask



ProtoJSON: `string`.

## google.protobuf.FloatValue



ProtoJSON: `number | "NaN" | "Infinity" | "-Infinity"`.

## google.protobuf.Int32Value



ProtoJSON: `number`.

## google.protobuf.Int64Value



ProtoJSON: `string`.

## google.protobuf.ListValue



ProtoJSON: `JsonValue[]`.

## google.protobuf.StringValue



ProtoJSON: `string`.

## google.protobuf.Struct



ProtoJSON: `{ [key: string]: JsonValue }`.

## google.protobuf.Timestamp



ProtoJSON: `string`.

## google.protobuf.UInt32Value



ProtoJSON: `number`.

## google.protobuf.UInt64Value



ProtoJSON: `string`.

## google.protobuf.Value



ProtoJSON: `JsonValue`.

## tabforge.demo.chat.ChatEvent



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| delta | 1 | tabforge.demo.chat.TextDelta | oneof payload |  |
| completed | 2 | tabforge.demo.chat.Completed | oneof payload |  |
| failed | 3 | tabforge.demo.chat.Failed | oneof payload |  |

## tabforge.demo.chat.ChatRequest



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| prompt | 1 | string | default may be omitted |  |
| conversationId | 2 | uint64 | default may be omitted |  |

## tabforge.demo.chat.ChatResponse



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| text | 1 | string | default may be omitted |  |

## tabforge.demo.chat.Completed



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| response | 1 | tabforge.demo.chat.ChatResponse | explicit presence |  |

## tabforge.demo.chat.Failed



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| message | 1 | string | default may be omitted |  |

## tabforge.demo.chat.TextDelta



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| text | 1 | string | default may be omitted |  |

## tabforge.demo.common.Reward

Shared by configuration files and other application messages.

| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| itemId | 3 | int32 | default may be omitted |  |
| count | 9 | uint64 | default may be omitted |  |

## tabforge.demo.config.Item



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| id | 7 | int32 | default may be omitted |  |
| name | 12 | string | default may be omitted |  |
| reward | 20 | tabforge.demo.common.Reward | explicit presence |  |
| costs | 24 | repeated tabforge.demo.common.Reward | default may be omitted |  |
| levels | 30 | repeated int32 | default may be omitted |  |
| stats | 35 | map<string, int32> | default may be omitted |  |
| rarity | 40 | tabforge.demo.common.Rarity | default may be omitted |  |
| iconHash | 45 | bytes | default may be omitted |  |
| unlockLevel | 50 | int32 | explicit presence | Empty cell is absent; an explicit zero retains presence. |
| textEffect | 55 | string | oneof effect |  |
| powerEffect | 60 | int32 | oneof effect |  |
| enabled | 61 | bool | default may be omitted |  |
| signedTotal | 62 | int64 | default may be omitted |  |
| weight | 63 | float | default may be omitted |  |
| price | 64 | double | default may be omitted |  |
| labels | 65 | repeated string | default may be omitted |  |
| ownerId | 66 | uint64 | default may be omitted |  |
| stock | 67 | uint32 | default may be omitted |  |
| createdAt | 68 | google.protobuf.Timestamp | explicit presence |  |
| ttl | 69 | google.protobuf.Duration | explicit presence |  |
| extra | 70 | google.protobuf.Struct | explicit presence |  |
| mask | 71 | google.protobuf.FieldMask | explicit presence |  |
| metadata | 72 | google.protobuf.Any | explicit presence |  |
| priority | 73 | google.protobuf.Int32Value | explicit presence |  |
| switches | 74 | repeated bool | default may be omitted |  |
| bonuses | 75 | map<string, tabforge.demo.common.Reward> | default may be omitted |  |

## tabforge.demo.config.Settings



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| maxLevel | 8 | int32 | default may be omitted |  |

## tabforge.demo.config.Tables



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| items | 10 | repeated tabforge.demo.config.Item | default may be omitted |  |
| byId | 20 | map<int32, tabforge.demo.config.Item> | default may be omitted |  |
| settings | 30 | tabforge.demo.config.Settings | explicit presence |  |

## tabforge.demo.structures.ScalarVariants



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| signedZigzag | 1 | sint64 | default may be omitted |  |
| unsignedFixed | 2 | fixed32 | default may be omitted |  |
| signedFixed | 3 | sfixed32 | default may be omitted |  |
| unsignedFixed64 | 4 | fixed64 | default may be omitted |  |
| signedFixed64 | 5 | sfixed64 | default may be omitted |  |

## tabforge.demo.structures.Tree

Not referenced by any RPC: pure data generation must still include this type.

| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| name | 1 | string | default may be omitted |  |
| children | 2 | repeated tabforge.demo.structures.Tree | default may be omitted |  |
| state | 3 | tabforge.demo.structures.Tree.State | default may be omitted |  |
| position | 4 | tabforge.demo.structures.Tree.Position | explicit presence |  |

## tabforge.demo.structures.Tree.Position



| Field | Number | Type | Presence | Description |
| --- | --- | --- | --- | --- |
| x | 1 | sint32 | default may be omitted |  |
| y | 2 | sint32 | default may be omitted |  |

## google.protobuf.NullValue

| Name | Number |
| --- | --- |
| NULL_VALUE | 0 |

## tabforge.demo.common.Rarity

| Name | Number |
| --- | --- |
| UNKNOWN | 0 |
| NORMAL | 1 |
| EPIC | 2 |

## tabforge.demo.structures.Tree.State

| Name | Number |
| --- | --- |
| IDLE | 0 |
| ACTIVE | 1 |


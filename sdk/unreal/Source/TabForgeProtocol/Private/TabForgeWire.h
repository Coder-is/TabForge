#pragma once
#include "CoreMinimal.h"
#include "Dom/JsonObject.h"
#include "Internationalization/Regex.h"

namespace TabForgeWire {
inline TSharedPtr<FJsonObject> Object(const TSharedPtr<FJsonValue>& Value) { return Value.IsValid() && Value->Type == EJson::Object ? Value->AsObject() : nullptr; }
inline FString String(const TSharedPtr<FJsonObject>& Object, const FString& Key) { FString Value; if (Object.IsValid()) Object->TryGetStringField(Key, Value); return Value; }
inline bool Matches(const FString& Pattern, const FString& Value) { FRegexMatcher Matcher(FRegexPattern(Pattern), Value); return Matcher.FindNext() && Matcher.GetMatchBeginning() == 0 && Matcher.GetMatchEnding() == Value.Len(); }
inline bool Integer(const FString& Value, bool Signed, int32 Bits = 64) {
    if (Value.IsEmpty() || Value == TEXT("-0")) return false;
    const bool Negative = Value.StartsWith(TEXT("-"));
    if (Negative && !Signed) return false;
    const FString Digits = Negative ? Value.Mid(1) : Value;
    if (Digits.IsEmpty() || (Digits.Len() > 1 && Digits[0] == '0')) return false;
    for (TCHAR Ch : Digits) if (Ch < '0' || Ch > '9') return false;
    const FString Max = Bits == 32 ? (Signed ? (Negative ? TEXT("2147483648") : TEXT("2147483647")) : TEXT("4294967295")) : (Signed ? (Negative ? TEXT("9223372036854775808") : TEXT("9223372036854775807")) : TEXT("18446744073709551615"));
    return Digits.Len() < Max.Len() || (Digits.Len() == Max.Len() && Digits.Compare(Max, ESearchCase::CaseSensitive) <= 0);
}
class Validator {
    TSharedPtr<FJsonObject> Schema;
    int32 Nodes = 0;
    bool Fail(const FString& Path, const FString& Expected) { Error = Path + TEXT(": expected ") + Expected; return false; }
    bool Budget(const FString& Path, int32 Depth) { return (Depth <= 64 && ++Nodes <= 100000) || Fail(Path, TEXT("bounded message depth/size")); }
    TSharedPtr<FJsonObject> FieldObject(const TSharedPtr<FJsonObject>& Parent, const FString& Field) { return Parent.IsValid() ? Object(Parent->TryGetField(Field)) : nullptr; }
    bool Json(const TSharedPtr<FJsonValue>& Value, const FString& Path, int32 Depth) {
        if (!Budget(Path, Depth) || !Value.IsValid()) return false;
        if (Value->Type == EJson::Object) { for (const auto& Pair : Value->AsObject()->Values) if (!Json(Pair.Value, Path, Depth + 1)) return false; return true; }
        if (Value->Type == EJson::Array) { for (const auto& Item : Value->AsArray()) if (!Json(Item, Path, Depth + 1)) return false; return true; }
        return Value->Type == EJson::Null || Value->Type == EJson::String || Value->Type == EJson::Boolean || (Value->Type == EJson::Number && FMath::IsFinite(Value->AsNumber())) || Fail(Path, TEXT("finite JSON value"));
    }
    bool Scalar(const TSharedPtr<FJsonObject>& Field, const TSharedPtr<FJsonValue>& Value, const FString& Path, int32 Depth) {
        if (!Budget(Path, Depth) || !Value.IsValid()) return false;
        const FString Kind = String(Field, TEXT("kind")), Type = String(Field, TEXT("type"));
        if (Kind == TEXT("message") || Kind == TEXT("group")) return Message(Type, Value, Path, Depth + 1);
        if (Kind == TEXT("enum")) {
            if (Type == TEXT("google.protobuf.NullValue") && Value->Type == EJson::Null) return true;
            if (Value->Type == EJson::Number) { double Number = Value->AsNumber(); return (FMath::IsFinite(Number) && Number >= -2147483648.0 && Number <= 2147483647.0 && FMath::FloorToDouble(Number) == Number) || Fail(Path, TEXT("enum int32")); }
            const auto Enums = FieldObject(Schema, TEXT("enums")); const auto Names = Enums.IsValid() ? Enums->TryGetField(Type) : nullptr;
            if (Value->Type == EJson::String && Names.IsValid() && Names->Type == EJson::Array) for (const auto& Name : Names->AsArray()) if (Name->AsString() == Value->AsString()) return true;
            return Fail(Path, TEXT("declared enum name"));
        }
        if (Kind.EndsWith(TEXT("64")) && Kind != TEXT("float64")) return (Value->Type == EJson::String && Integer(Value->AsString(), Kind != TEXT("uint64") && Kind != TEXT("fixed64"))) || Fail(Path, TEXT("decimal int64 string"));
        if (Kind == TEXT("int32") || Kind == TEXT("sint32") || Kind == TEXT("sfixed32") || Kind == TEXT("uint32") || Kind == TEXT("fixed32")) {
            const bool Signed = Kind != TEXT("uint32") && Kind != TEXT("fixed32");
            if (Value->Type != EJson::Number) return Fail(Path, TEXT("int32/uint32"));
            const double Number = Value->AsNumber();
            return (FMath::IsFinite(Number) && FMath::FloorToDouble(Number) == Number && Number >= (Signed ? -2147483648.0 : 0.0) && Number <= (Signed ? 2147483647.0 : 4294967295.0)) || Fail(Path, TEXT("int32/uint32 range"));
        }
        if (Kind == TEXT("float") || Kind == TEXT("double")) {
            if (Value->Type == EJson::String) return (Value->AsString() == TEXT("NaN") || Value->AsString() == TEXT("Infinity") || Value->AsString() == TEXT("-Infinity")) || Fail(Path, TEXT("float special value"));
            return (Value->Type == EJson::Number && FMath::IsFinite(Value->AsNumber()) && (Kind != TEXT("float") || FMath::Abs(Value->AsNumber()) <= 3.4028234663852886e38)) || Fail(Path, TEXT("float/double"));
        }
        if (Kind == TEXT("bool")) return Value->Type == EJson::Boolean || Fail(Path, TEXT("boolean"));
        if (Kind == TEXT("string") || Kind == TEXT("bytes")) return (Value->Type == EJson::String && (Kind != TEXT("bytes") || Matches(TEXT("^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$"), Value->AsString()))) || Fail(Path, TEXT("string/Base64"));
        return Fail(Path, TEXT("supported scalar"));
    }
    bool Message(const FString& Type, const TSharedPtr<FJsonValue>& Value, const FString& Path, int32 Depth) {
        if (!Budget(Path, Depth) || !Value.IsValid()) return false;
        if (Type.StartsWith(TEXT("google.protobuf."))) {
            const FString Name = Type.Mid(16);
            static const TMap<FString, FString> Wrappers { {TEXT("DoubleValue"), TEXT("double")}, {TEXT("FloatValue"), TEXT("float")}, {TEXT("Int64Value"), TEXT("int64")}, {TEXT("UInt64Value"), TEXT("uint64")}, {TEXT("Int32Value"), TEXT("int32")}, {TEXT("UInt32Value"), TEXT("uint32")}, {TEXT("BoolValue"), TEXT("bool")}, {TEXT("StringValue"), TEXT("string")}, {TEXT("BytesValue"), TEXT("bytes")} };
            if (const FString* Kind = Wrappers.Find(Name)) { auto Shape = MakeShared<FJsonObject>(); Shape->SetStringField(TEXT("kind"), *Kind); return Scalar(Shape, Value, Path, Depth + 1); }
            if (Name == TEXT("Timestamp")) {
                if (Value->Type != EJson::String || !Matches(TEXT("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?Z$"), Value->AsString())) return Fail(Path, TEXT("canonical UTC timestamp"));
                const FString Text = Value->AsString(); const int32 Year = FCString::Atoi(*Text.Left(4)), Month = FCString::Atoi(*Text.Mid(5, 2)), Day = FCString::Atoi(*Text.Mid(8, 2));
                static const int32 Days[] = {31,28,31,30,31,30,31,31,30,31,30,31};
                const bool Leap = Year % 4 == 0 && (Year % 100 != 0 || Year % 400 == 0);
                return (Year >= 1 && Month >= 1 && Month <= 12 && Day >= 1 && Day <= Days[Month - 1] + (Month == 2 && Leap ? 1 : 0) && FCString::Atoi(*Text.Mid(11,2)) <= 23 && FCString::Atoi(*Text.Mid(14,2)) <= 59 && FCString::Atoi(*Text.Mid(17,2)) <= 59) || Fail(Path, TEXT("valid UTC timestamp"));
            }
            if (Name == TEXT("Duration")) {
                if (Value->Type != EJson::String || !Matches(TEXT("^-?(0|[1-9][0-9]*)(?:\\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?s$"), Value->AsString())) return Fail(Path, TEXT("canonical duration"));
                FString Seconds = Value->AsString().LeftChop(1); Seconds.RemoveFromStart(TEXT("-")); int32 Dot; if (Seconds.FindChar('.', Dot)) Seconds = Seconds.Left(Dot);
                return (Seconds.Len() < 12 || (Seconds.Len() == 12 && Seconds.Compare(TEXT("315576000000"), ESearchCase::CaseSensitive) <= 0)) || Fail(Path, TEXT("duration range"));
            }
            if (Name == TEXT("FieldMask")) return (Value->Type == EJson::String && Matches(TEXT("^(?:[A-Za-z][A-Za-z0-9]*(?:\\.[A-Za-z][A-Za-z0-9]*)*(?:,[A-Za-z][A-Za-z0-9]*(?:\\.[A-Za-z][A-Za-z0-9]*)*)*)?$"), Value->AsString())) || Fail(Path, TEXT("field mask"));
            if (Name == TEXT("Value")) return Json(Value, Path, Depth + 1);
            if (Name == TEXT("Struct")) return (Value->Type == EJson::Object && Json(Value, Path, Depth + 1)) || Fail(Path, TEXT("JSON object"));
            if (Name == TEXT("ListValue")) return (Value->Type == EJson::Array && Json(Value, Path, Depth + 1)) || Fail(Path, TEXT("JSON array"));
            if (Name == TEXT("Any")) {
                auto Fields = Object(Value); FString Url;
                if (!Fields.IsValid() || !Fields->TryGetStringField(TEXT("@type"), Url)) return Fail(Path, TEXT("Any @type"));
                int32 Slash; const FString NestedType = Url.FindLastChar('/', Slash) ? Url.Mid(Slash + 1) : Url;
                const auto Messages = FieldObject(Schema, TEXT("messages"));
                if (NestedType == Type || !Messages.IsValid() || !Messages->HasField(NestedType)) return Fail(Path, TEXT("known Any type"));
                auto Nested = MakeShared<FJsonObject>(); Nested->Values = Fields->Values; Nested->RemoveField(TEXT("@type"));
                if (NestedType.StartsWith(TEXT("google.protobuf.")) && NestedType != TEXT("google.protobuf.Empty")) return (Nested->Values.Num() == 1 && Nested->HasField(TEXT("value")) && Message(NestedType, Nested->TryGetField(TEXT("value")), Path, Depth + 1)) || Fail(Path, TEXT("Any value"));
                return Message(NestedType, MakeShared<FJsonValueObject>(Nested), Path, Depth + 1);
            }
        }
        const auto Messages = FieldObject(Schema, TEXT("messages")); const auto Shape = FieldObject(Messages, Type); const auto Fields = Object(Value);
        if (!Shape.IsValid() || !Fields.IsValid()) return Fail(Path, TEXT("known message object"));
        const auto Oneofs = Shape->TryGetField(TEXT("oneofs"));
        if (Oneofs.IsValid() && Oneofs->Type == EJson::Array) for (const auto& Group : Oneofs->AsArray()) { int32 Found = 0; for (const auto& Member : Group->AsArray()) if (Fields->HasField(Member->AsString())) ++Found; if (Found > 1) return Fail(Path, TEXT("one oneof member")); }
        const auto Declared = FieldObject(Shape, TEXT("fields")); if (!Declared.IsValid()) return Fail(Path, TEXT("field schema"));
        for (const auto& Pair : Declared->Values) { bool Required = false; const auto Field = Object(Pair.Value); if (Field.IsValid() && Field->TryGetBoolField(TEXT("required"), Required) && Required && !Fields->HasField(Pair.Key)) return Fail(Path, TEXT("required field ") + Pair.Key); }
        for (const auto& Pair : Fields->Values) {
            const auto Field = FieldObject(Declared, Pair.Key); const FString Next = Path + TEXT(".") + Pair.Key;
            if (!Field.IsValid()) return Fail(Next, TEXT("declared field"));
            const FString KeyKind = String(Field, TEXT("mapKey")); bool List = false; Field->TryGetBoolField(TEXT("list"), List);
            if (!KeyKind.IsEmpty()) {
                const auto Entries = Object(Pair.Value); if (!Entries.IsValid()) return Fail(Next, TEXT("map"));
                for (const auto& Item : Entries->Values) {
                    if (KeyKind == TEXT("bool")) { if (Item.Key != TEXT("true") && Item.Key != TEXT("false")) return Fail(Next, TEXT("boolean map key")); }
                    else if (KeyKind != TEXT("string") && !Integer(Item.Key, KeyKind.StartsWith(TEXT("s")) || KeyKind.StartsWith(TEXT("int")), KeyKind.EndsWith(TEXT("32")) ? 32 : 64)) return Fail(Next, TEXT("integer map key"));
                    if (!Scalar(Field, Item.Value, Next, Depth + 1)) return false;
                }
            } else if (List) { if (Pair.Value->Type != EJson::Array) return Fail(Next, TEXT("array")); for (const auto& Item : Pair.Value->AsArray()) if (!Scalar(Field, Item, Next, Depth + 1)) return false; }
            else if (!Scalar(Field, Pair.Value, Next, Depth + 1)) return false;
        }
        return true;
    }
public:
    FString Error;
    explicit Validator(TSharedPtr<FJsonObject> InSchema) : Schema(MoveTemp(InSchema)) {}
    bool Validate(const FString& Type, const TSharedPtr<FJsonValue>& Value) { Nodes = 0; Error.Empty(); return Message(Type, Value, TEXT("$"), 0); }
};
}

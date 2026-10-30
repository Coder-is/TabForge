#pragma once
#include "CoreMinimal.h"
#include "Dom/JsonObject.h"
#include "Dom/JsonValue.h"
#include "Internationalization/Regex.h"
#include "TabForgeJsonSyntax.h"

// ProtoJSON validation following Go protojson's input rules. See
// sdk/typescript/schema.ts for the reference implementation shared by all SDKs.
namespace TabForgeWire {
constexpr int32 JsonMaxDepth = 64;
constexpr int32 ProtocolMaxNodes = 100000;  // network messages (also capped at 1 MiB)
constexpr int32 DataMaxNodes = 10000000;    // every data bundle loader uses this budget

inline TSharedPtr<FJsonObject> Object(const TSharedPtr<FJsonValue>& Value) { return Value.IsValid() && Value->Type == EJson::Object ? Value->AsObject() : nullptr; }
inline FString String(const TSharedPtr<FJsonObject>& Object, const FString& Key) { FString Value; if (Object.IsValid()) Object->TryGetStringField(Key, Value); return Value; }
inline bool Same(const FString& A, const FString& B) { return A.Equals(B, ESearchCase::CaseSensitive); }
// FJsonObject keys compare case-insensitively; input members must match exactly.
inline TSharedPtr<FJsonValue> Exact(const TSharedPtr<FJsonObject>& Object, const FString& Key) {
    if (!Object.IsValid() || !Object->Values.Contains(Key)) return nullptr;
    for (const auto& Pair : Object->Values) if (Same(Pair.Key, Key)) return Pair.Value;
    return nullptr;
}
inline bool Matches(const FString& Pattern, const FString& Value) { FRegexMatcher Matcher(FRegexPattern(Pattern), Value); return Matcher.FindNext() && Matcher.GetMatchBeginning() == 0 && Matcher.GetMatchEnding() == Value.Len(); }
inline bool Digit(TCHAR Ch) { return Ch >= '0' && Ch <= '9'; }
inline bool InRange(const FString& Digits, bool Negative, bool Signed, int32 Bits) {
    if (Digits.IsEmpty() || (Negative && !Signed)) return false;
    const FString Max = Bits == 32 ? (Signed ? (Negative ? TEXT("2147483648") : TEXT("2147483647")) : TEXT("4294967295")) : (Signed ? (Negative ? TEXT("9223372036854775808") : TEXT("9223372036854775807")) : TEXT("18446744073709551615"));
    return Digits.Len() < Max.Len() || (Digits.Len() == Max.Len() && Digits.Compare(Max, ESearchCase::CaseSensitive) <= 0);
}
// Canonical decimal string, the form 64-bit integers are emitted in.
inline bool Integer(const FString& Value, bool Signed, int32 Bits = 64) {
    if (Value.IsEmpty() || Value == TEXT("-0")) return false;
    const bool Negative = Value[0] == '-';
    const FString Digits = Negative ? Value.Mid(1) : Value;
    if (Digits.IsEmpty() || (Digits.Len() > 1 && Digits[0] == '0')) return false;
    for (TCHAR Ch : Digits) if (!Digit(Ch)) return false;
    return InRange(Digits, Negative, Signed, Bits);
}
// Splits JSON number grammar; Int is empty for a leading "0".
inline bool NumberParts(const FString& Text, bool& Negative, FString& Int, FString& Frac, FString& Exp) {
    int32 I = 0; const int32 N = Text.Len(); Negative = false; Int.Empty(); Frac.Empty(); Exp.Empty();
    if (I < N && Text[I] == '-') { Negative = true; ++I; }
    if (I >= N) return false;
    if (Text[I] == '0') ++I;
    else if (Text[I] >= '1' && Text[I] <= '9') { const int32 S = I; while (I < N && Digit(Text[I])) ++I; Int = Text.Mid(S, I - S); }
    else return false;
    if (I < N && Text[I] == '.') { const int32 S = ++I; while (I < N && Digit(Text[I])) ++I; if (I == S) return false; Frac = Text.Mid(S, I - S); }
    if (I < N && (Text[I] == 'e' || Text[I] == 'E')) {
        const int32 S = ++I; if (I < N && (Text[I] == '+' || Text[I] == '-')) ++I;
        const int32 D = I; while (I < N && Digit(Text[I])) ++I; if (I == D) return false; Exp = Text.Mid(S, I - S);
    }
    return I == N;
}
inline bool NumberText(const FString& Text) { bool Negative; FString Int, Frac, Exp; return NumberParts(Text, Negative, Int, Frac, Exp); }
// Go protojson integer input (normalizeToIntString): exact after shifting the
// exponent, with Go's 20-digit cap for positive exponents. Out is canonical.
inline bool ProtoInteger(const FString& Text, bool Signed, int32 Bits, FString& Out) {
    bool Negative; FString Int, Frac, Exp;
    if (!NumberParts(Text, Negative, Int, Frac, Exp)) return false;
    while (!Frac.IsEmpty() && Frac[Frac.Len() - 1] == '0') Frac.LeftChopInline(1);
    if (Int.IsEmpty() && Frac.IsEmpty()) { Out = TEXT("0"); return true; }
    int64 Exponent = 0;
    if (!Exp.IsEmpty()) {
        const bool ExpNegative = Exp[0] == '-';
        for (int32 K = (Exp[0] == '+' || Exp[0] == '-') ? 1 : 0; K < Exp.Len(); ++K) { Exponent = Exponent * 10 + (Exp[K] - '0'); if (Exponent > 2147483648LL) return false; }
        if (ExpNegative) Exponent = -Exponent;
        if (Exponent > 2147483647LL) return false;
    }
    FString Digits;
    if (Exponent >= 0) {
        if (Frac.Len() > Exponent || Int.Len() + Exponent > 20) return false;
        Digits = Int + Frac + FString::ChrN(static_cast<int32>(Exponent) - Frac.Len(), TEXT('0'));
    } else {
        const int64 Index = Int.Len() + Exponent;
        if (!Frac.IsEmpty() || Index < 0) return false;
        for (int32 K = static_cast<int32>(Index); K < Int.Len(); ++K) if (Int[K] != '0') return false;
        Digits = Int.Left(static_cast<int32>(Index));
    }
    int32 Lead = 0; while (Lead + 1 < Digits.Len() && Digits[Lead] == '0') ++Lead;
    Digits = Digits.Mid(Lead);
    if (!InRange(Digits, Negative, Signed, Bits)) return false;
    Out = Negative ? TEXT("-") + Digits : Digits;
    return true;
}
inline bool IntegerKind(const FString& Kind, bool& Signed, int32& Bits) {
    if (Kind == TEXT("int32") || Kind == TEXT("sint32") || Kind == TEXT("sfixed32")) { Signed = true; Bits = 32; return true; }
    if (Kind == TEXT("uint32") || Kind == TEXT("fixed32")) { Signed = false; Bits = 32; return true; }
    if (Kind == TEXT("int64") || Kind == TEXT("sint64") || Kind == TEXT("sfixed64")) { Signed = true; Bits = 64; return true; }
    if (Kind == TEXT("uint64") || Kind == TEXT("fixed64")) { Signed = false; Bits = 64; return true; }
    return false;
}
// Go strconv.ParseInt/ParseUint(key, 10, bits) for map keys; Out is canonical.
inline bool ProtoMapKey(const FString& Key, const FString& Kind, FString& Out) {
    if (Kind == TEXT("string")) { Out = Key; return true; }
    if (Kind == TEXT("bool")) { Out = Key; return Same(Key, TEXT("true")) || Same(Key, TEXT("false")); }
    bool Signed = false; int32 Bits = 64;
    if (!IntegerKind(Kind, Signed, Bits)) return false;
    int32 Start = 0; bool Negative = false;
    if (!Key.IsEmpty() && (Key[0] == '+' || Key[0] == '-')) { if (!Signed) return false; Negative = Key[0] == '-'; Start = 1; }
    if (Start == Key.Len()) return false;
    for (int32 K = Start; K < Key.Len(); ++K) if (!Digit(Key[K])) return false;
    int32 Lead = Start; while (Lead + 1 < Key.Len() && Key[Lead] == '0') ++Lead;
    const FString Digits = Key.Mid(Lead);
    Negative = Negative && Digits != TEXT("0");
    if (!InRange(Digits, Negative, Signed, Bits)) return false;
    Out = Negative ? TEXT("-") + Digits : Digits;
    return true;
}
// Go protojson bytes input: URL alphabet when "-" or "_" occurs, padded exactly
// when the length is a multiple of 4, CR/LF ignored, unused bits ignored.
// Out is canonical padded standard Base64.
inline bool ProtoBytes(const FString& Value, FString& Out) {
    static const TCHAR* Alphabet = TEXT("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/");
    bool Url = false; for (TCHAR Ch : Value) if (Ch == '-' || Ch == '_') Url = true;
    const bool Padded = Value.Len() % 4 == 0;
    TArray<int32> Sextets; Sextets.Reserve(Value.Len()); int32 Pad = 0;
    for (TCHAR Ch : Value) {
        if (Ch == '\r' || Ch == '\n') continue;
        if (Ch == '=') { if (!Padded) return false; ++Pad; continue; }
        if (Pad > 0) return false;
        int32 Sextet = Ch >= 'A' && Ch <= 'Z' ? Ch - 'A' : Ch >= 'a' && Ch <= 'z' ? Ch - 'a' + 26 : Ch >= '0' && Ch <= '9' ? Ch - '0' + 52 : -1;
        if (Sextet < 0) Sextet = Url ? (Ch == '-' ? 62 : Ch == '_' ? 63 : -1) : (Ch == '+' ? 62 : Ch == '/' ? 63 : -1);
        if (Sextet < 0) return false;
        Sextets.Add(Sextet);
    }
    const int32 Rest = Sextets.Num() % 4;
    if (Rest == 1 || (Padded && !((Rest == 0 && Pad == 0) || (Rest == 2 && Pad == 2) || (Rest == 3 && Pad == 1)))) return false;
    if (Rest == 2) Sextets.Last() &= 0x30;
    if (Rest == 3) Sextets.Last() &= 0x3c;
    Out.Empty(Sextets.Num() + 2);
    for (int32 Sextet : Sextets) Out.AppendChar(Alphabet[Sextet]);
    if (Rest == 2) Out += TEXT("==");
    if (Rest == 3) Out += TEXT("=");
    return true;
}

// A parsed document plus the exact source text of every number in it.
struct FJsonDocument {
    TSharedPtr<FJsonValue> Root;
    TMap<const FJsonValue*, FString> Numbers;
    TArray<TSharedPtr<FJsonValue>> NumberValues; // keeps Numbers keys alive
};
inline FString Utf8Text(const std::string& Text) { FUTF8ToTCHAR Converted(Text.data(), static_cast<int32>(Text.size())); return FString(Converted.Length(), Converted.Get()); }
class FJsonBuilder final : public tabforge::JsonVisitor {
    struct FFrame { TSharedPtr<FJsonObject> Object; TArray<TSharedPtr<FJsonValue>> Array; FString Key; };
    TArray<FFrame> Stack;
    FJsonDocument& Document;
    bool Add(const TSharedPtr<FJsonValue>& Value) {
        if (Stack.Num() == 0) { Document.Root = Value; return true; }
        FFrame& Top = Stack.Last();
        if (Top.Object.IsValid()) Top.Object->SetField(Top.Key, Value); else Top.Array.Add(Value);
        return true;
    }
public:
    explicit FJsonBuilder(FJsonDocument& InDocument) : Document(InDocument) {}
    bool begin_object() override { FFrame Frame; Frame.Object = MakeShared<FJsonObject>(); Stack.Add(MoveTemp(Frame)); return true; }
    bool key(const std::string& Name) override {
        // FJsonObject cannot hold members that differ only by case; reject instead of overwriting.
        FFrame& Top = Stack.Last(); Top.Key = Utf8Text(Name);
        return !Top.Object->Values.Contains(Top.Key);
    }
    bool end_object() override { FFrame Frame = Stack.Pop(); return Add(MakeShared<FJsonValueObject>(Frame.Object)); }
    bool begin_array() override { Stack.Add(FFrame()); return true; }
    bool end_array() override { FFrame Frame = Stack.Pop(); return Add(MakeShared<FJsonValueArray>(Frame.Array)); }
    bool string_value(const std::string& Value) override { return Add(MakeShared<FJsonValueString>(Utf8Text(Value))); }
    bool number_value(const std::string& Text) override {
        const FString Raw = Utf8Text(Text);
        TSharedPtr<FJsonValue> Value = MakeShared<FJsonValueNumber>(FCString::Atod(*Raw));
        Document.Numbers.Add(Value.Get(), Raw); Document.NumberValues.Add(Value);
        return Add(Value);
    }
    bool bool_value(bool Value) override { return Add(MakeShared<FJsonValueBoolean>(Value)); }
    bool null_value() override { return Add(MakeShared<FJsonValueNull>()); }
};
// Strict UTF-8/JSON parse that builds the tree itself, so numbers keep their text.
inline bool Parse(const std::string& Text, FJsonDocument& Document, int32 MaxDepth = JsonMaxDepth, int32 MaxNodes = ProtocolMaxNodes) {
    Document = FJsonDocument();
    FJsonBuilder Builder(Document);
    return tabforge::JsonSyntax(Text, static_cast<std::size_t>(MaxDepth), static_cast<std::size_t>(MaxNodes), &Builder).valid() && Document.Root.IsValid();
}
inline TSharedPtr<FJsonValue> Clone(const TSharedPtr<FJsonValue>& Value) {
    if (!Value.IsValid()) return Value;
    if (Value->Type == EJson::Object) { auto Copy = MakeShared<FJsonObject>(); for (const auto& Pair : Value->AsObject()->Values) Copy->SetField(Pair.Key, Clone(Pair.Value)); return MakeShared<FJsonValueObject>(Copy); }
    if (Value->Type == EJson::Array) { TArray<TSharedPtr<FJsonValue>> Copy; for (const auto& Item : Value->AsArray()) Copy.Add(Clone(Item)); return MakeShared<FJsonValueArray>(Copy); }
    return Value; // scalar values are immutable
}

// Depth equals the JSON nesting depth of each value and every JSON value counts
// at most once, so for parsed text the parser's budget is the binding one.
class Validator {
    TSharedPtr<FJsonObject> Schema;
    int32 MaxDepth, MaxNodes;
    bool bNormalize;
    int64 Nodes = 0;
    const TMap<const FJsonValue*, FString>* Numbers = nullptr;
    bool Fail(const FString& Path, const FString& Expected) { if (Error.IsEmpty()) Error = Path + TEXT(": expected ") + Expected; return false; }
    bool Budget(const FString& Path, int32 Depth) { return (Depth <= MaxDepth && ++Nodes <= MaxNodes) || Fail(Path, TEXT("bounded message depth/size")); }
    TSharedPtr<FJsonObject> FieldObject(const TSharedPtr<FJsonObject>& Parent, const FString& Field) { return Parent.IsValid() ? Object(Parent->TryGetField(Field)) : nullptr; }
    static bool Nullable(const FString& Kind, const FString& Type) { return (Kind == TEXT("message") && Type == TEXT("google.protobuf.Value")) || (Kind == TEXT("enum") && Type == TEXT("google.protobuf.NullValue")); }
    // Exact source text of a number; without it, the exact value of the double.
    bool NumberSource(const TSharedPtr<FJsonValue>& Value, FString& Out) const {
        if (Value->Type == EJson::String) { Out = Value->AsString(); return true; }
        if (Value->Type != EJson::Number) return false;
        if (Numbers) if (const FString* Raw = Numbers->Find(Value.Get())) { Out = *Raw; return true; }
        const double Number = Value->AsNumber();
        if (!FMath::IsFinite(Number)) return false;
        if (FMath::FloorToDouble(Number) == Number && Number >= -9223372036854775808.0 && Number < 18446744073709551616.0)
            Out = Number >= 0 ? FString::Printf(TEXT("%llu"), static_cast<unsigned long long>(Number)) : FString::Printf(TEXT("%lld"), static_cast<long long>(Number));
        else Out = FString::Printf(TEXT("%.17g"), Number);
        return true;
    }
    bool Json(const TSharedPtr<FJsonValue>& Value, const FString& Path, int32 Depth, bool Counted) {
        if (!Value.IsValid()) return Fail(Path, TEXT("JSON value"));
        if (!Counted && !Budget(Path, Depth)) return false;
        if (Value->Type == EJson::Object) { for (const auto& Pair : Value->AsObject()->Values) if (!Json(Pair.Value, Path + TEXT(".") + Pair.Key, Depth + 1, false)) return false; return true; }
        if (Value->Type == EJson::Array) { for (const auto& Item : Value->AsArray()) if (!Json(Item, Path, Depth + 1, false)) return false; return true; }
        return Value->Type == EJson::Null || Value->Type == EJson::String || Value->Type == EJson::Boolean || (Value->Type == EJson::Number && FMath::IsFinite(Value->AsNumber())) || Fail(Path, TEXT("finite JSON value"));
    }
    bool Scalar(const FString& Kind, const FString& Type, const TSharedPtr<FJsonValue>& Value, const FString& Path, int32 Depth, bool Counted, TSharedPtr<FJsonValue>& Out) {
        Out = Value;
        if (!Value.IsValid()) return Fail(Path, TEXT("JSON value"));
        if (Kind == TEXT("message") || Kind == TEXT("group")) return Message(Type, Value, Path, Depth, Counted, Out);
        if (!Counted && !Budget(Path, Depth)) return false;
        bool Signed = false; int32 Bits = 64; FString Canonical;
        if (Kind == TEXT("enum")) {
            if (Type == TEXT("google.protobuf.NullValue") && Value->Type == EJson::Null) return true;
            if (Value->Type == EJson::String) {
                const auto Enums = FieldObject(Schema, TEXT("enums")); const auto Names = Enums.IsValid() ? Enums->TryGetField(Type) : nullptr;
                if (Names.IsValid() && Names->Type == EJson::Array) for (const auto& Name : Names->AsArray()) if (Name.IsValid() && Name->Type == EJson::String && Same(Name->AsString(), Value->AsString())) return true;
                return Fail(Path, TEXT("declared enum name"));
            }
            FString Number;
            if (Value->Type != EJson::Number || !NumberSource(Value, Number) || !ProtoInteger(Number, true, 32, Canonical)) return Fail(Path, TEXT("enum name or int32"));
            Out = MakeShared<FJsonValueNumber>(FCString::Atod(*Canonical)); return true;
        }
        if (IntegerKind(Kind, Signed, Bits)) {
            FString Number;
            if (!NumberSource(Value, Number) || !ProtoInteger(Number, Signed, Bits, Canonical)) return Fail(Path, Kind);
            if (Bits == 64) Out = MakeShared<FJsonValueString>(Canonical); else Out = MakeShared<FJsonValueNumber>(FCString::Atod(*Canonical));
            return true;
        }
        if (Kind == TEXT("float") || Kind == TEXT("double")) {
            double Number = 0;
            if (Value->Type == EJson::String) {
                const FString Special = Value->AsString();
                if (Same(Special, TEXT("NaN")) || Same(Special, TEXT("Infinity")) || Same(Special, TEXT("-Infinity"))) return true;
                if (!NumberText(Special)) return Fail(Path, Kind);
                Number = FCString::Atod(*Special);
            } else if (Value->Type == EJson::Number) Number = Value->AsNumber();
            else return Fail(Path, Kind);
            // Go emits float32 max as 3.4028235e+38; accept values that round to a finite float32.
            if (!FMath::IsFinite(Number) || (Kind == TEXT("float") && FMath::Abs(Number) >= 3.4028235677973366e38)) return Fail(Path, Kind);
            Out = MakeShared<FJsonValueNumber>(Number); return true;
        }
        if (Kind == TEXT("bool")) return Value->Type == EJson::Boolean || Fail(Path, TEXT("boolean"));
        if (Kind == TEXT("string")) return Value->Type == EJson::String || Fail(Path, TEXT("string"));
        if (Kind == TEXT("bytes")) {
            if (Value->Type != EJson::String || !ProtoBytes(Value->AsString(), Canonical)) return Fail(Path, TEXT("Base64 string"));
            Out = MakeShared<FJsonValueString>(Canonical); return true;
        }
        return Fail(Path, TEXT("supported scalar"));
    }
    // Handled is false for google.protobuf types without a special JSON form.
    bool WellKnown(const FString& Type, const TSharedPtr<FJsonValue>& Value, const FString& Path, int32 Depth, TSharedPtr<FJsonValue>& Out, bool& Handled) {
        Handled = true;
        const FString Name = Type.Mid(16);
        static const TMap<FString, FString> Wrappers { {TEXT("DoubleValue"), TEXT("double")}, {TEXT("FloatValue"), TEXT("float")}, {TEXT("Int64Value"), TEXT("int64")}, {TEXT("UInt64Value"), TEXT("uint64")}, {TEXT("Int32Value"), TEXT("int32")}, {TEXT("UInt32Value"), TEXT("uint32")}, {TEXT("BoolValue"), TEXT("bool")}, {TEXT("StringValue"), TEXT("string")}, {TEXT("BytesValue"), TEXT("bytes")} };
        if (const FString* Kind = Wrappers.Find(Name)) return Scalar(*Kind, FString(), Value, Path, Depth, true, Out);
        if (Name == TEXT("Timestamp")) {
            if (Value->Type != EJson::String || !Matches(TEXT("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?Z$"), Value->AsString())) return Fail(Path, TEXT("canonical UTC timestamp"));
            const FString Stamp = Value->AsString(); const int32 Year = FCString::Atoi(*Stamp.Left(4)), Month = FCString::Atoi(*Stamp.Mid(5, 2)), Day = FCString::Atoi(*Stamp.Mid(8, 2));
            static const int32 Days[] = {31,28,31,30,31,30,31,31,30,31,30,31};
            const bool Leap = Year % 4 == 0 && (Year % 100 != 0 || Year % 400 == 0);
            return (Year >= 1 && Month >= 1 && Month <= 12 && Day >= 1 && Day <= Days[Month - 1] + (Month == 2 && Leap ? 1 : 0) && FCString::Atoi(*Stamp.Mid(11,2)) <= 23 && FCString::Atoi(*Stamp.Mid(14,2)) <= 59 && FCString::Atoi(*Stamp.Mid(17,2)) <= 59) || Fail(Path, TEXT("valid UTC timestamp"));
        }
        if (Name == TEXT("Duration")) {
            if (Value->Type != EJson::String || !Matches(TEXT("^-?(0|[1-9][0-9]*)(?:\\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?s$"), Value->AsString())) return Fail(Path, TEXT("canonical duration"));
            FString Seconds = Value->AsString().LeftChop(1); Seconds.RemoveFromStart(TEXT("-")); int32 Dot; if (Seconds.FindChar('.', Dot)) Seconds = Seconds.Left(Dot);
            return (Seconds.Len() < 12 || (Seconds.Len() == 12 && Seconds.Compare(TEXT("315576000000"), ESearchCase::CaseSensitive) <= 0)) || Fail(Path, TEXT("duration range"));
        }
        if (Name == TEXT("FieldMask")) return (Value->Type == EJson::String && Matches(TEXT("^(?:[A-Za-z][A-Za-z0-9]*(?:\\.[A-Za-z][A-Za-z0-9]*)*(?:,[A-Za-z][A-Za-z0-9]*(?:\\.[A-Za-z][A-Za-z0-9]*)*)*)?$"), Value->AsString())) || Fail(Path, TEXT("field mask"));
        if (Name == TEXT("Value")) return Json(Value, Path, Depth, true);
        if (Name == TEXT("Struct")) {
            if (Value->Type != EJson::Object) return Fail(Path, TEXT("JSON object"));
            for (const auto& Pair : Value->AsObject()->Values) if (!Json(Pair.Value, Path + TEXT(".") + Pair.Key, Depth + 1, false)) return false;
            return true;
        }
        if (Name == TEXT("ListValue")) {
            if (Value->Type != EJson::Array) return Fail(Path, TEXT("JSON array"));
            for (const auto& Item : Value->AsArray()) if (!Json(Item, Path, Depth + 1, false)) return false;
            return true;
        }
        if (Name == TEXT("Any")) {
            const auto Fields = Object(Value);
            if (!Fields.IsValid()) return Fail(Path, TEXT("Any object"));
            if (Fields->Values.Num() == 0) return true; // protojson writes an unset Any as {}
            const auto Url = Exact(Fields, TEXT("@type"));
            if (!Url.IsValid() || Url->Type != EJson::String) return Fail(Path, TEXT("Any @type"));
            int32 Slash; const FString NestedType = Url->AsString().FindLastChar('/', Slash) ? Url->AsString().Mid(Slash + 1) : Url->AsString();
            if (Same(NestedType, Type) || !Exact(FieldObject(Schema, TEXT("messages")), NestedType).IsValid()) return Fail(Path, TEXT("known Any type"));
            const int32 Members = Fields->Values.Num() - 1;
            if (Same(NestedType, TEXT("google.protobuf.Empty")) && Members == 0) return true; // Go tolerates an omitted Empty value
            if (NestedType.StartsWith(TEXT("google.protobuf."), ESearchCase::CaseSensitive)) {
                const auto Nested = Exact(Fields, TEXT("value"));
                if (Members != 1 || !Nested.IsValid()) return Fail(Path, TEXT("Any value"));
                TSharedPtr<FJsonValue> Normalized;
                if (!Message(NestedType, Nested, Path + TEXT(".value"), Depth + 1, false, Normalized)) return false;
                if (bNormalize) Fields->SetField(TEXT("value"), Normalized);
                return true;
            }
            TSharedPtr<FJsonValue> Ignored;
            return Message(NestedType, Value, Path, Depth, true, Ignored, true);
        }
        Handled = false;
        return true;
    }
    bool Message(const FString& Type, const TSharedPtr<FJsonValue>& Value, const FString& Path, int32 Depth, bool Counted, TSharedPtr<FJsonValue>& Out, bool SkipType = false) {
        Out = Value;
        if (!Value.IsValid()) return Fail(Path, TEXT("JSON value"));
        if (!Counted && !Budget(Path, Depth)) return false;
        if (Type.StartsWith(TEXT("google.protobuf."), ESearchCase::CaseSensitive)) { bool Handled = false; const bool Valid = WellKnown(Type, Value, Path, Depth, Out, Handled); if (Handled) return Valid; }
        const auto Shape = FieldObject(FieldObject(Schema, TEXT("messages")), Type); const auto Fields = Object(Value);
        if (!Shape.IsValid() || !Fields.IsValid()) return Fail(Path, TEXT("known message object"));
        const auto Declared = FieldObject(Shape, TEXT("fields")); if (!Declared.IsValid()) return Fail(Path, TEXT("field schema"));
        // JSON null means "absent" except where null is itself a value.
        auto Present = [&](const FString& Name) {
            const auto Item = Exact(Fields, Name); if (!Item.IsValid()) return false;
            const auto Field = Object(Exact(Declared, Name));
            return Item->Type != EJson::Null || !Field.IsValid() || Nullable(String(Field, TEXT("kind")), String(Field, TEXT("type")));
        };
        const auto Oneofs = Shape->TryGetField(TEXT("oneofs"));
        if (Oneofs.IsValid() && Oneofs->Type == EJson::Array) for (const auto& Group : Oneofs->AsArray()) { int32 Found = 0; for (const auto& Member : Group->AsArray()) if (Present(Member->AsString())) ++Found; if (Found > 1) return Fail(Path, TEXT("one oneof member")); }
        for (const auto& Pair : Declared->Values) { bool Required = false; const auto Field = Object(Pair.Value); if (Field.IsValid() && Field->TryGetBoolField(TEXT("required"), Required) && Required && !Present(Pair.Key)) return Fail(Path + TEXT(".") + Pair.Key, TEXT("required field")); }
        TArray<FString> Absent;
        for (auto& Pair : Fields->Values) {
            if (SkipType && Same(Pair.Key, TEXT("@type"))) continue;
            const FString Next = Path + TEXT(".") + Pair.Key;
            const auto Field = Object(Exact(Declared, Pair.Key));
            if (!Field.IsValid()) return Fail(Next, TEXT("declared field"));
            if (!Pair.Value.IsValid()) return Fail(Next, TEXT("JSON value"));
            const FString Kind = String(Field, TEXT("kind")), FieldType = String(Field, TEXT("type")), KeyKind = String(Field, TEXT("mapKey"));
            bool List = false; Field->TryGetBoolField(TEXT("list"), List);
            if (Pair.Value->Type == EJson::Null && !Nullable(Kind, FieldType)) { Absent.Add(Pair.Key); continue; }
            if (!KeyKind.IsEmpty()) {
                const auto Entries = Object(Pair.Value); if (!Entries.IsValid()) return Fail(Next, TEXT("map"));
                if (!Budget(Next, Depth + 1)) return false;
                TSet<FString> Seen; bool Renamed = false; auto Rebuilt = MakeShared<FJsonObject>();
                for (auto& Item : Entries->Values) {
                    FString Canonical;
                    if (!ProtoMapKey(Item.Key, KeyKind, Canonical)) return Fail(Next, KeyKind + TEXT(" map key"));
                    if (Seen.Contains(Canonical)) return Fail(Next, TEXT("unique map key"));
                    Seen.Add(Canonical); Renamed = Renamed || !Same(Canonical, Item.Key);
                    TSharedPtr<FJsonValue> Normalized;
                    if (!Scalar(Kind, FieldType, Item.Value, Next + TEXT(".") + Item.Key, Depth + 2, false, Normalized)) return false;
                    if (bNormalize) { Item.Value = Normalized; Rebuilt->SetField(Canonical, Normalized); }
                }
                if (bNormalize && Renamed) Pair.Value = MakeShared<FJsonValueObject>(Rebuilt);
            } else if (List) {
                if (Pair.Value->Type != EJson::Array) return Fail(Next, TEXT("array"));
                if (!Budget(Next, Depth + 1)) return false;
                TArray<TSharedPtr<FJsonValue>> Items; int32 Index = 0;
                for (const auto& Item : Pair.Value->AsArray()) {
                    TSharedPtr<FJsonValue> Normalized;
                    if (!Scalar(Kind, FieldType, Item, FString::Printf(TEXT("%s[%d]"), *Next, Index++), Depth + 2, false, Normalized)) return false;
                    Items.Add(Normalized);
                }
                if (bNormalize) Pair.Value = MakeShared<FJsonValueArray>(Items);
            } else {
                TSharedPtr<FJsonValue> Normalized;
                if (!Scalar(Kind, FieldType, Pair.Value, Next, Depth + 1, false, Normalized)) return false;
                if (bNormalize) Pair.Value = Normalized;
            }
        }
        if (bNormalize) for (const FString& Key : Absent) Fields->RemoveField(Key);
        return true;
    }
public:
    FString Error;
    // Canonical ProtoJSON after a successful Validate when normalizing.
    TSharedPtr<FJsonValue> Result;
    // Defaults are the network budget without rewriting; data loaders pass DataMaxNodes and normalize.
    explicit Validator(TSharedPtr<FJsonObject> InSchema, int32 InMaxDepth = JsonMaxDepth, int32 InMaxNodes = ProtocolMaxNodes, bool bInNormalize = false)
        : Schema(MoveTemp(InSchema)), MaxDepth(InMaxDepth), MaxNodes(InMaxNodes), bNormalize(bInNormalize) {}
    bool Validate(const FString& Type, const TSharedPtr<FJsonValue>& Value, const TMap<const FJsonValue*, FString>* InNumbers = nullptr) {
        Nodes = 0; Error.Empty(); Numbers = InNumbers; Result.Reset();
        TSharedPtr<FJsonValue> Out;
        const bool Valid = Message(Type, Value, TEXT("$"), 0, false, Out);
        Numbers = nullptr;
        if (Valid) Result = bNormalize ? Out : Value;
        return Valid;
    }
};
}

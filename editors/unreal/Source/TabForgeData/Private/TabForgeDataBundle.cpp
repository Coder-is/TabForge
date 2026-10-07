#include "TabForgeDataBundle.h"
#include "TabForgeWire.h"
#include "TabForgeJsonSyntax.h"
#include "Misc/FileHelper.h"
#include "Misc/Paths.h"
#include "Modules/ModuleManager.h"
#include "Serialization/JsonReader.h"
#include "Serialization/JsonSerializer.h"

IMPLEMENT_MODULE(FDefaultModuleImpl, TabForgeData)

namespace {
bool SafePath(const FString& Path) {
    if (Path.IsEmpty() || Path.Contains(TEXT("\\")) || Path.Contains(TEXT(":")) || !FPaths::IsRelative(Path)) return false;
    TArray<FString> Parts; Path.ParseIntoArray(Parts, TEXT("/"), false);
    for (const FString& Part : Parts) if (Part.IsEmpty() || Part == TEXT(".") || Part == TEXT("..")) return false;
    return true;
}
bool Parse(const FString& Json, TSharedPtr<FJsonObject>& Object, FString& Error) {
    FTCHARToUTF8 Utf8(*Json);
    const std::string Text(Utf8.Get(), Utf8.Length());
    if (!tabforge::JsonSyntax(Text).valid() || !FJsonSerializer::Deserialize(TJsonReaderFactory<>::Create(Json), Object) || !Object.IsValid()) {
        Error = TEXT("Invalid, duplicate-key or oversized JSON object"); return false;
    }
    return true;
}
bool Load(const FString& Path, FString& Json, TSharedPtr<FJsonObject>& Object, FString& Error) {
    TArray<uint8> Bytes;
    if (!FFileHelper::LoadFileToArray(Bytes, *Path)) { Error = TEXT("Cannot read ") + Path; return false; }
    if (Bytes.IsEmpty()) { Error = TEXT("Empty JSON file: ") + Path; return false; }
    const std::string Raw(reinterpret_cast<const char*>(Bytes.GetData()), Bytes.Num());
    if (!tabforge::JsonSyntax(Raw).valid()) { Error = TEXT("Invalid UTF-8 or JSON: ") + Path; return false; }
    FUTF8ToTCHAR Text(Raw.data(), static_cast<int32>(Raw.size()));
    Json = FString(Text.Length(), Text.Get());
    return Parse(Json, Object, Error);
}
}

TSharedPtr<FTabForgeDataBundle> FTabForgeDataBundle::Open(const FString& Directory, FString& Error, const FString& ExpectedSchemaHash) {
    Error.Empty();
    FString Json; TSharedPtr<FJsonObject> Manifest, Schema;
    if (!Load(FPaths::Combine(Directory,TEXT("bundle.json")),Json,Manifest,Error)) return nullptr;
    FString Format, Hash;
    if (!Manifest->TryGetStringField(TEXT("format"),Format) || Format != TEXT("tabforge.unreal.data.v1") || !Manifest->TryGetStringField(TEXT("schemaHash"),Hash) || Hash.IsEmpty() || (!ExpectedSchemaHash.IsEmpty() && Hash != ExpectedSchemaHash)) {
        Error = TEXT("Data bundle format/schema identity mismatch"); return nullptr;
    }
    if (!Load(FPaths::Combine(Directory,TEXT("wire_schema.json")),Json,Schema,Error)) return nullptr;
    const TArray<TSharedPtr<FJsonValue>>* Entries = nullptr;
    if (!Manifest->TryGetArrayField(TEXT("data"),Entries)) { Error = TEXT("Missing data entries"); return nullptr; }
    auto Bundle = MakeShared<FTabForgeDataBundle>(); Bundle->Hash = Hash;
    TSet<FString> Seen;
    for (const auto& Value : *Entries) {
        if (!Value.IsValid() || Value->Type != EJson::Object) { Error = TEXT("Invalid data entry"); return nullptr; }
        auto Entry = Value->AsObject(); FString Path, Message;
        if (!Entry->TryGetStringField(TEXT("path"),Path) || !Entry->TryGetStringField(TEXT("message"),Message) || !SafePath(Path) || !Path.EndsWith(TEXT(".json"),ESearchCase::CaseSensitive) || Seen.Contains(Path.ToLower())) {
            Error = TEXT("Unsafe or duplicate data entry"); return nullptr;
        }
        Seen.Add(Path.ToLower()); TSharedPtr<FJsonObject> Data;
        if (!Load(FPaths::Combine(Directory,Path),Json,Data,Error)) return nullptr;
        TabForgeWire::Validator Validator(Schema);
        if (!Validator.Validate(Message,MakeShared<FJsonValueObject>(Data))) { Error = Path + TEXT(": ") + Validator.Error; return nullptr; }
        Bundle->Documents.Add(Path,Json);
    }
    return Bundle;
}

TSharedPtr<FJsonObject> FTabForgeDataBundle::Read(const FString& RelativePath, FString& Error) const {
    Error.Empty(); TSharedPtr<FJsonObject> Object;
    const FString* Json = Documents.Find(RelativePath);
    if (!Json) { Error = TEXT("Data file is not registered: ") + RelativePath; return nullptr; }
    if (!Parse(*Json,Object,Error)) return nullptr;
    return Object; // Fresh object; callers cannot mutate another reader's data.
}

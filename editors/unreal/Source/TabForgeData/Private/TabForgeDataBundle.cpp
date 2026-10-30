#include "TabForgeDataBundle.h"
#include "TabForgeWire.h"
#include "TabForgeJsonSyntax.h"
#include "Misc/FileHelper.h"
#include "Misc/Paths.h"
#include "Modules/ModuleManager.h"

IMPLEMENT_MODULE(FDefaultModuleImpl, TabForgeData)

namespace {
bool SafePath(const FString& Path) {
    if (Path.IsEmpty() || Path.Contains(TEXT("\\")) || Path.Contains(TEXT(":")) || !FPaths::IsRelative(Path)) return false;
    TArray<FString> Parts; Path.ParseIntoArray(Parts, TEXT("/"), false);
    for (const FString& Part : Parts) if (Part.IsEmpty() || Part == TEXT(".") || Part == TEXT("..")) return false;
    return true;
}
// Every document of a bundle uses the data budget shared by all TabForge data
// loaders (depth 64, 10,000,000 JSON values), so a bundle loads everywhere or nowhere.
bool Load(const FString& Path, TabForgeWire::FJsonDocument& Document, FString& Error) {
    TArray<uint8> Bytes;
    if (!FFileHelper::LoadFileToArray(Bytes, *Path)) { Error = TEXT("Cannot read ") + Path; return false; }
    const std::string Raw(reinterpret_cast<const char*>(Bytes.GetData()), Bytes.Num());
    if (!TabForgeWire::Parse(Raw, Document, TabForgeWire::JsonMaxDepth, TabForgeWire::DataMaxNodes)) { Error = TEXT("Invalid UTF-8, duplicate-key or oversized JSON: ") + Path; return false; }
    if (Document.Root->Type != EJson::Object) { Error = TEXT("Expected a JSON object: ") + Path; return false; }
    return true;
}
}

TSharedPtr<FTabForgeDataBundle> FTabForgeDataBundle::Open(const FString& Directory, FString& Error, const FString& ExpectedSchemaHash) {
    Error.Empty();
    TabForgeWire::FJsonDocument ManifestDocument, SchemaDocument;
    if (!Load(FPaths::Combine(Directory,TEXT("bundle.json")),ManifestDocument,Error)) return nullptr;
    const TSharedPtr<FJsonObject> Manifest = ManifestDocument.Root->AsObject();
    FString Format, Hash;
    if (!Manifest->TryGetStringField(TEXT("format"),Format) || Format != TEXT("tabforge.unreal.data.v1") || !Manifest->TryGetStringField(TEXT("schemaHash"),Hash) || Hash.IsEmpty() || (!ExpectedSchemaHash.IsEmpty() && !Hash.Equals(ExpectedSchemaHash, ESearchCase::CaseSensitive))) {
        Error = TEXT("Data bundle format/schema identity mismatch"); return nullptr;
    }
    if (!Load(FPaths::Combine(Directory,TEXT("wire_schema.json")),SchemaDocument,Error)) return nullptr;
    const TSharedPtr<FJsonObject> Schema = SchemaDocument.Root->AsObject();
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
        Seen.Add(Path.ToLower());
        TabForgeWire::FJsonDocument Data;
        if (!Load(FPaths::Combine(Directory,Path),Data,Error)) return nullptr;
        // Raw number text reaches the validator; stored data is canonical ProtoJSON
        // (64-bit integers as decimal strings, padded Base64, null members removed).
        TabForgeWire::Validator Validator(Schema, TabForgeWire::JsonMaxDepth, TabForgeWire::DataMaxNodes, true);
        if (!Validator.Validate(Message, Data.Root, &Data.Numbers)) { Error = Path + TEXT(": ") + Validator.Error; return nullptr; }
        const TSharedPtr<FJsonObject> Canonical = TabForgeWire::Object(Validator.Result);
        if (!Canonical.IsValid()) { Error = Path + TEXT(": expected a message object"); return nullptr; }
        Bundle->Documents.Add(Path, Canonical);
    }
    return Bundle;
}

TSharedPtr<FJsonObject> FTabForgeDataBundle::Read(const FString& RelativePath, FString& Error) const {
    Error.Empty();
    const TSharedPtr<FJsonObject>* Document = Documents.Find(RelativePath);
    if (!Document) { Error = TEXT("Data file is not registered: ") + RelativePath; return nullptr; }
    return TabForgeWire::Object(TabForgeWire::Clone(MakeShared<FJsonValueObject>(*Document))); // fresh copy per reader
}

#pragma once
#include "CoreMinimal.h"
#include "Dom/JsonObject.h"

// Immutable in-memory snapshot. Opening a replacement never changes a previous
// snapshot; keep the old shared pointer when Open reports an error.
class TABFORGEDATA_API FTabForgeDataBundle {
public:
    static TSharedPtr<FTabForgeDataBundle> Open(const FString& Directory, FString& Error, const FString& ExpectedSchemaHash = TEXT(""));
    TSharedPtr<FJsonObject> Read(const FString& RelativePath, FString& Error) const;
    FString SchemaHash() const { return Hash; }
private:
    FString Hash;
    TMap<FString, FString> Documents;
};

#pragma once
#include "CoreMinimal.h"
#include "Dom/JsonObject.h"
#include "Dom/JsonValue.h"
#include <atomic>

struct FTabForgeError { FString Code, Message; bool Retryable = false; };
struct FTabForgeEvent { FString Name, RequestId, Sequence; TSharedPtr<FJsonObject> Payload; };
struct FTabForgeCallbacks {
    TFunction<void(const FTabForgeEvent&)> OnEvent;
    TFunction<void(const TSharedPtr<FJsonValue>&)> OnComplete;
    TFunction<void(const FTabForgeError&)> OnError;
};
struct FTabForgeOptions {
    int32 MaxResponseBytes = 1048576;
    int32 MaxFrameBytes = 1048576;
    int32 MaxQueuedBytes = 1048576;
    int32 MaxQueuedChunks = 128;
    int32 MaxConcurrentRequests = 16;
};

class FTabForgeClient;
class TABFORGEPROTOCOL_API FTabForgeRequest : public TSharedFromThis<FTabForgeRequest, ESPMode::ThreadSafe> {
public:
    FTabForgeRequest();
    ~FTabForgeRequest();
    void Cancel(); // Thread safe; HTTP cleanup/callbacks occur on the game thread.
    bool IsDone() const;
    FString RequestId() const;
private:
    friend class FTabForgeClient;
    struct FState;
    TUniquePtr<FState> State;
    std::atomic<bool> Cancelled { false }, Done { false };
    void Begin(const FString& BaseURL, const TSharedPtr<FJsonObject>& Contract, const FString& Operation, const TSharedPtr<FJsonValue>& Data, FTabForgeCallbacks Callbacks, const FString& Token, const FString& Id, bool Streaming, const FTabForgeOptions& Options, bool OverLimit);
    bool Tick(float Delta);
    bool Enqueue(const void* Bytes, int64 Count);
    void Fail(const FString& Code, const FString& Message, bool Retryable = false);
    void Complete(const TSharedPtr<FJsonValue>& Value);
};

/// Create/send/cancel-all on the game thread. Immutable generated metadata is
/// shared by calls; all user callbacks run on the game thread via the core ticker.
class TABFORGEPROTOCOL_API FTabForgeClient {
public:
    static TSharedPtr<FTabForgeClient, ESPMode::ThreadSafe> Create(const FString& BaseURL, const FString& RuntimeJson, FTabForgeError& Error, FTabForgeOptions Options = {});
    ~FTabForgeClient();
    TSharedRef<FTabForgeRequest, ESPMode::ThreadSafe> Call(const FString& Operation, const TSharedPtr<FJsonValue>& Data, FTabForgeCallbacks Callbacks, const FString& Token = TEXT(""), const FString& RequestId = TEXT(""));
    TSharedRef<FTabForgeRequest, ESPMode::ThreadSafe> Stream(const FString& Operation, const TSharedPtr<FJsonValue>& Data, FTabForgeCallbacks Callbacks, const FString& Token = TEXT(""), const FString& RequestId = TEXT(""));
    void CancelAll();
private:
    FString BaseURL;
    TSharedPtr<FJsonObject> Contract;
    FTabForgeOptions Options;
    TArray<TWeakPtr<FTabForgeRequest, ESPMode::ThreadSafe>> Requests;
    TSharedRef<FTabForgeRequest, ESPMode::ThreadSafe> Send(const FString& Operation, const TSharedPtr<FJsonValue>& Data, FTabForgeCallbacks Callbacks, const FString& Token, const FString& Id, bool Streaming);
};

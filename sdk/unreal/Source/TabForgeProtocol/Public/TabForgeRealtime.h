#pragma once
#include "CoreMinimal.h"
#include "Dom/JsonObject.h"
#include "Dom/JsonValue.h"
#include "TabForgeClient.h"

// Realtime channel client (doc/realtime.md) on the engine WebSocket module:
// requests, pushes and notifies over binary Protobuf frames
// (tabforge.realtime.v1.proto), automatic reconnect and session resume with
// retransmission, so a request runs at most once and pushes arrive once, in
// order. Messages are ProtoJSON FJsonValue/FJsonObject, checked against the
// generated runtime.json like FTabForgeClient (64-bit integers as strings).
//
// Create, call and destroy on the game thread. Every callback runs on the game
// thread from the core ticker; methods never invoke callbacks synchronously.

enum class ETabForgeRealtimeState : uint8 {
    Idle,
    Connecting,
    Connected,    // a new session started: reload game state
    Resumed,      // the previous session continued; nothing was lost
    Disconnected, // reconnecting with backoff
    Closed        // permanent: kicked, duplicate login, unauthorized, version mismatch or Close()
};

struct FTabForgeRealtimeOptions {
    FString Url;     // full WebSocket URL including the channel path, e.g. wss://host/ws/game
    FString Channel; // channel id from contract.json
    TFunction<FString()> Token; // bearer token for a new session; resumes do not send it again
    TMap<FString, FString> UpgradeHeaders;
    int32 MinBackoffMS = 200;
    int32 MaxBackoffMS = 10000;
    int32 MaxUnacked = 1024; // frames kept until the server acknowledges them
};

struct FTabForgeRealtimeCallbacks {
    TFunction<void(const TSharedPtr<FJsonObject>&)> OnResponse;
    TFunction<void(const FTabForgeError&)> OnError; // business errors keep the server's code
};

class TABFORGEPROTOCOL_API FTabForgeRealtimeClient {
public:
    static TSharedPtr<FTabForgeRealtimeClient, ESPMode::ThreadSafe> Create(const FString& RuntimeJson, FTabForgeRealtimeOptions Options, FTabForgeError& Error);
    ~FTabForgeRealtimeClient();

    // State changes; Error is set for Disconnected/Closed caused by an error.
    TFunction<void(ETabForgeRealtimeState, const FTabForgeError*)> OnState;
    // Pushes whose payload fails decoding or validation (they are skipped).
    TFunction<void(const FTabForgeError&)> OnHandlerError;

    bool Connect(FTabForgeError& Error);
    // TimeoutMS 0 uses the contract timeout; a smaller value shortens it.
    bool Request(const FString& Name, const TSharedPtr<FJsonValue>& Message, FTabForgeRealtimeCallbacks Callbacks, FTabForgeError& Error, int32 TimeoutMS = 0);
    // Reliable notifies queue while offline; volatile ones fail with "dropped".
    bool Notify(const FString& Name, const TSharedPtr<FJsonValue>& Message, FTabForgeError& Error);
    // Subscribes to a push by name; unknown pushes from newer servers are ignored.
    bool OnPush(const FString& Name, TFunction<void(const TSharedPtr<FJsonObject>&)> Handler, FTabForgeError& Error);
    // Ends the session on the server and stops reconnecting.
    void Close();

    ETabForgeRealtimeState GetState() const;
    FString GetSessionId() const;

    struct FState;
private:
    FTabForgeRealtimeClient() = default;
    TSharedPtr<FState, ESPMode::ThreadSafe> State;
};

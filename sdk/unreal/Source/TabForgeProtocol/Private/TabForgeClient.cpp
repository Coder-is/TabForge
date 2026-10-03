#include "TabForgeClient.h"
#include "TabForgeSse.h"
#include "TabForgeJsonSyntax.h"
#include "TabForgeWire.h"
#include "HttpModule.h"
#include "Interfaces/IHttpRequest.h"
#include "Interfaces/IHttpResponse.h"
#include "Containers/Ticker.h"
#include "HAL/PlatformTime.h"
#include "Misc/ScopeLock.h"
#include "Serialization/Archive.h"
#include "Serialization/JsonReader.h"
#include "Serialization/JsonSerializer.h"
#include "Policies/CondensedJsonPrintPolicy.h"

namespace {
std::string Bytes(const FString& Text) { FTCHARToUTF8 UTF8(*Text); return std::string(UTF8.Get(), UTF8.Length()); }
FString Text(const std::string& UTF8) { FUTF8ToTCHAR Converted(UTF8.data(), static_cast<int32>(UTF8.size())); return FString(Converted.Length(), Converted.Get()); }
TSharedPtr<FJsonValue> Parse(const std::string& Input) {
    if (!tabforge::JsonSyntax(Input).valid()) return nullptr;
    auto Reader = TJsonReaderFactory<>::Create(Text(Input)); TSharedPtr<FJsonValue> Value;
    return FJsonSerializer::Deserialize(Reader, Value) ? Value : nullptr;
}
FString Serialize(const TSharedPtr<FJsonValue>& Value) {
    FString JSON; auto Writer = TJsonWriterFactory<TCHAR, TCondensedJsonPrintPolicy<TCHAR>>::Create(&JSON);
    return FJsonSerializer::Serialize(Value, TEXT(""), Writer) ? JSON : FString();
}
class FReceiveArchive : public FArchive {
    TFunction<bool(const void*, int64)> Receive;
    int64 Position = 0;
public:
    explicit FReceiveArchive(TFunction<bool(const void*, int64)> InReceive) : Receive(MoveTemp(InReceive)) { ArIsSaving = true; }
    void Serialize(void* Data, int64 Count) override { if (Count < 0 || !Receive(Data, Count)) SetError(); else Position += Count; }
    int64 Tell() override { return Position; }
};
}

struct FTabForgeRequest::FState {
    FCriticalSection Mutex;
    TArray<TArray<uint8>> Pending;
    int64 PendingBytes = 0;
    int32 Status = 0;
    FString ContentType;
    bool NetworkComplete = false, NetworkSuccess = false, Streaming = false, CheckedHeaders = false;
    FTabForgeError PendingError;
    FString Id, Version, Hash, Sequence = TEXT("0");
    TSharedPtr<FJsonObject> Operation, Schema;
    FHttpRequestPtr Network;
    FTabForgeOptions Options;
    FTabForgeCallbacks Callbacks;
    TUniquePtr<tabforge::SseParser> Parser;
    std::string Body;
    double Deadline = 0;
    bool Envelope(const TSharedPtr<FJsonObject>& Object, FTabForgeError& Error) const {
        if (!Object.IsValid()) { Error = { TEXT("invalid_json"), TEXT("Expected response object"), false }; return false; }
        if (TabForgeWire::String(Object, TEXT("protocolVersion")) != Version) { Error = { TEXT("version_mismatch"), TEXT("Response version differs"), false }; return false; }
        if (TabForgeWire::String(Object, TEXT("schemaHash")) != Hash) { Error = { TEXT("schema_mismatch"), TEXT("Response schema differs"), false }; return false; }
        if (TabForgeWire::String(Object, TEXT("requestId")) != Id) { Error = { TEXT("invalid_frame"), TEXT("Response request ID differs"), false }; return false; }
        return true;
    }
    FTabForgeError RemoteError(const TSharedPtr<FJsonValue>& Value) const {
        auto Object = TabForgeWire::Object(Value); FTabForgeError Error;
        if (!Object.IsValid() || !Object->TryGetStringField(TEXT("code"), Error.Code) || !Object->TryGetStringField(TEXT("message"), Error.Message) || !Object->TryGetBoolField(TEXT("retryable"), Error.Retryable)) return { TEXT("invalid_frame"), TEXT("Invalid transport error"), false };
        return Error;
    }
};

FTabForgeRequest::FTabForgeRequest() : State(MakeUnique<FState>()) {}
FTabForgeRequest::~FTabForgeRequest() = default;
void FTabForgeRequest::Cancel() { Cancelled.store(true); }
bool FTabForgeRequest::IsDone() const { return Done.load(); }
FString FTabForgeRequest::RequestId() const { return State->Id; }

bool FTabForgeRequest::Enqueue(const void* Data, int64 Count) {
    if (Done.load() || Cancelled.load()) return false;
    FScopeLock Lock(&State->Mutex);
    if (Count < 0 || Count > State->Options.MaxQueuedBytes || State->PendingBytes + Count > State->Options.MaxQueuedBytes || State->Pending.Num() >= State->Options.MaxQueuedChunks) {
        State->PendingError = { TEXT("backpressure"), TEXT("HTTP producer exceeded bounded game-thread queue"), false }; return false;
    }
    if (Count > 0) { TArray<uint8> Chunk; Chunk.Append(static_cast<const uint8*>(Data), static_cast<int32>(Count)); State->Pending.Add(MoveTemp(Chunk)); State->PendingBytes += Count; }
    return true;
}
void FTabForgeRequest::Fail(const FString& Code, const FString& Message, bool Retryable) {
    if (Done.exchange(true)) return;
    if (State->Network.IsValid()) State->Network->CancelRequest();
    if (State->Callbacks.OnError) State->Callbacks.OnError({ Code, Message, Retryable });
}
void FTabForgeRequest::Complete(const TSharedPtr<FJsonValue>& Value) {
    if (Done.exchange(true)) return;
    if (State->Network.IsValid()) State->Network->CancelRequest();
    if (State->Callbacks.OnComplete) State->Callbacks.OnComplete(Value);
}

void FTabForgeRequest::Begin(const FString& URL, const TSharedPtr<FJsonObject>& Contract, const FString& OperationId, const TSharedPtr<FJsonValue>& Data, FTabForgeCallbacks Callbacks, const FString& Token, const FString& Id, bool Streaming, const FTabForgeOptions& Options, bool OverLimit) {
    State->Callbacks = MoveTemp(Callbacks); State->Options = Options; State->Streaming = Streaming;
    State->Id = Id.IsEmpty() ? FGuid::NewGuid().ToString(EGuidFormats::Digits) : Id;
    State->Version = TabForgeWire::String(Contract, TEXT("version")); State->Hash = TabForgeWire::String(Contract, TEXT("schemaHash"));
    State->Schema = TabForgeWire::Object(Contract->TryGetField(TEXT("wireSchema")));
    const auto Operations = TabForgeWire::Object(Contract->TryGetField(TEXT("operations")));
    State->Operation = Operations.IsValid() ? TabForgeWire::Object(Operations->TryGetField(OperationId)) : nullptr;
    auto Startup = [&](const FString& Code, const FString& Message) { FScopeLock Lock(&State->Mutex); State->PendingError = { Code, Message, false }; };
    if (OverLimit) Startup(TEXT("busy"), TEXT("Concurrent request limit reached"));
    else if (!State->Operation.IsValid() || TabForgeWire::String(State->Operation, TEXT("transport")) != (Streaming ? TEXT("http_sse") : TEXT("http_json"))) Startup(TEXT("invalid_operation"), TEXT("Unknown operation or wrong transport"));
    else if (!TabForgeWire::Matches(TEXT("^[A-Za-z0-9_.-]{1,128}$"), State->Id)) Startup(TEXT("bad_request"), TEXT("Invalid request ID"));
    else if (Token.Contains(TEXT("\r")) || Token.Contains(TEXT("\n"))) Startup(TEXT("bad_request"), TEXT("Invalid bearer token"));
    else if (TabForgeWire::String(State->Operation, TEXT("auth")) == TEXT("bearer") && Token.IsEmpty()) Startup(TEXT("unauthorized"), TEXT("Bearer token required"));
    else {
        TabForgeWire::Validator Validator(State->Schema);
        if (!Validator.Validate(TabForgeWire::String(State->Operation, TEXT("requestType")), Data)) Startup(TEXT("invalid_message"), Validator.Error);
    }
    const auto Self = AsShared();
    FTSTicker::GetCoreTicker().AddTicker(FTickerDelegate::CreateLambda([Self](float Delta) { return Self->Tick(Delta); }));
    if (!State->PendingError.Code.IsEmpty()) return;
    double Timeout = 0;
    if (!State->Operation->TryGetNumberField(TEXT("timeoutMS"), Timeout) || Timeout <= 0 || Timeout > 86400000) { Startup(TEXT("bad_request"), TEXT("Invalid timeout")); return; }
    State->Deadline = FPlatformTime::Seconds() + Timeout / 1000.0;
    State->Parser = MakeUnique<tabforge::SseParser>(Options.MaxFrameBytes);
    const FString Body = Serialize(Data);
    if (Body.IsEmpty()) { Startup(TEXT("invalid_message"), TEXT("Cannot serialize request")); return; }
    const auto Network = FHttpModule::Get().CreateRequest(); State->Network = Network;
    Network->SetURL(URL + TabForgeWire::String(State->Operation, TEXT("path"))); Network->SetVerb(TEXT("POST")); Network->SetContentAsString(Body);
    Network->SetTimeout(static_cast<float>(Timeout / 1000.0));
    Network->SetDelegateThreadPolicy(EHttpRequestDelegateThreadPolicy::CompleteOnHttpThread);
    Network->SetHeader(TEXT("Content-Type"), TEXT("application/json")); Network->SetHeader(TEXT("Accept"), Streaming ? TEXT("text/event-stream") : TEXT("application/json"));
    Network->SetHeader(TEXT("X-Protocol-Version"), State->Version); Network->SetHeader(TEXT("X-Protocol-Schema"), State->Hash); Network->SetHeader(TEXT("X-Request-ID"), State->Id);
    if (!Token.IsEmpty()) Network->SetHeader(TEXT("Authorization"), TEXT("Bearer ") + Token);
    const TWeakPtr<FTabForgeRequest, ESPMode::ThreadSafe> Weak = Self;
    Network->OnStatusCodeReceived().BindLambda([Weak](FHttpRequestPtr, int32 Status) { if (auto Request = Weak.Pin()) { FScopeLock Lock(&Request->State->Mutex); Request->State->Status = Status; } });
    Network->OnHeaderReceived().BindLambda([Weak](FHttpRequestPtr, const FString& Name, const FString& Value) { if (Name.Equals(TEXT("Content-Type"), ESearchCase::IgnoreCase)) if (auto Request = Weak.Pin()) { FScopeLock Lock(&Request->State->Mutex); Request->State->ContentType = Value; } });
    Network->OnProcessRequestComplete().BindLambda([Weak](FHttpRequestPtr, FHttpResponsePtr Response, bool Success) {
        if (auto Request = Weak.Pin()) {
            FScopeLock Lock(&Request->State->Mutex); Request->State->NetworkComplete = true; Request->State->NetworkSuccess = Success;
            if (Response.IsValid()) { Request->State->Status = Response->GetResponseCode(); Request->State->ContentType = Response->GetHeader(TEXT("Content-Type")); }
        }
    });
    auto Receiver = MakeShared<FReceiveArchive>([Weak](const void* Bytes, int64 Count) { auto Request = Weak.Pin(); return Request.IsValid() && Request->Enqueue(Bytes, Count); });
    if (!Network->SetResponseBodyReceiveStream(Receiver)) { Startup(TEXT("unsupported_transport"), TEXT("HTTP backend does not support response body streaming")); return; }
    if (!Network->ProcessRequest()) Startup(TEXT("transport_error"), TEXT("Cannot start HTTP request"));
}

bool FTabForgeRequest::Tick(float) {
    if (Done.load()) return false;
    if (Cancelled.load()) { Fail(TEXT("cancelled"), TEXT("Request cancelled")); return false; }
    TArray<TArray<uint8>> Chunks; FTabForgeError Error; int32 Status; FString ContentType; bool Finished, Success;
    {
        FScopeLock Lock(&State->Mutex); Error = State->PendingError; Status = State->Status; ContentType = State->ContentType; Finished = State->NetworkComplete; Success = State->NetworkSuccess;
        if (Status > 0 && !ContentType.IsEmpty()) { Chunks = MoveTemp(State->Pending); State->Pending.Reset(); State->PendingBytes = 0; }
    }
    if (!Error.Code.IsEmpty()) { Fail(Error.Code, Error.Message, Error.Retryable); return false; }
    if (FPlatformTime::Seconds() >= State->Deadline) { Fail(TEXT("timeout"), TEXT("Request deadline exceeded"), true); return false; }
    if (Status == 0 || ContentType.IsEmpty()) { if (Finished) { Fail(TEXT("transport_error"), TEXT("Response has no usable headers")); return false; } return true; }
    const bool IsStream = State->Streaming && Status >= 200 && Status < 300;
    if (!State->CheckedHeaders) {
        int32 Semicolon; if (ContentType.FindChar(';', Semicolon)) ContentType = ContentType.Left(Semicolon);
        if (ContentType.TrimStartAndEnd().ToLower() != (IsStream ? TEXT("text/event-stream") : TEXT("application/json"))) { Fail(TEXT("unsupported_media_type"), TEXT("Unexpected HTTP response content type")); return false; }
        State->CheckedHeaders = true;
    }
    for (const auto& Chunk : Chunks) {
        if (IsStream) {
            const bool Ok = State->Parser->feed(Chunk.GetData(), Chunk.Num(), [&](const tabforge::SseFrame& Frame) {
                const auto Object = TabForgeWire::Object(Parse(Frame.data)); FTabForgeError FrameError;
                if (!State->Envelope(Object, FrameError)) { Fail(FrameError.Code, FrameError.Message); return false; }
                const FString Next = Text(tabforge::next_sequence(Bytes(State->Sequence)));
                if (TabForgeWire::String(Object, TEXT("sequence")) != Next || Text(Frame.id) != Next) { Fail(TEXT("invalid_sequence"), TEXT("Sequence must increment from 1")); return false; }
                State->Sequence = Next; const FString Name = Text(Frame.event);
                if (Name == TEXT("protocol.error")) {
                    if (Object->HasField(TEXT("payload"))) { Fail(TEXT("invalid_frame"), TEXT("Error frame contains payload")); return false; }
                    const auto Remote = State->RemoteError(Object->TryGetField(TEXT("error"))); Fail(Remote.Code, Remote.Message, Remote.Retryable); return false;
                }
                const auto Events = TabForgeWire::Object(State->Operation->TryGetField(TEXT("events"))); const auto Descriptor = Events.IsValid() ? TabForgeWire::Object(Events->TryGetField(Name)) : nullptr;
                const auto Payload = TabForgeWire::Object(Object->TryGetField(TEXT("payload")));
                if (!Descriptor.IsValid() || Object->HasField(TEXT("error")) || !Payload.IsValid() || Payload->Values.Num() != 1 || !Payload->HasField(TabForgeWire::String(Descriptor, TEXT("field")))) { Fail(TEXT("invalid_event"), TEXT("Event differs from response oneof")); return false; }
                TabForgeWire::Validator Validator(State->Schema);
                if (!Validator.Validate(TabForgeWire::String(State->Operation, TEXT("responseType")), Object->TryGetField(TEXT("payload")))) { Fail(TEXT("invalid_message"), Validator.Error); return false; }
                if (State->Callbacks.OnEvent) State->Callbacks.OnEvent({ Name, State->Id, Next, Payload });
                bool Terminal = false; Descriptor->TryGetBoolField(TEXT("terminal"), Terminal);
                if (Terminal) { Complete(MakeShared<FJsonValueObject>(Payload)); return false; }
                return !Cancelled.load();
            });
            if (!Ok && !State->Parser->error.empty()) Fail(Text(State->Parser->error), TEXT("Invalid SSE stream"));
        } else {
            if (State->Body.size() + Chunk.Num() > static_cast<std::size_t>(State->Options.MaxResponseBytes)) { Fail(TEXT("response_too_large"), TEXT("Response exceeds configured limit")); return false; }
            State->Body.append(reinterpret_cast<const char*>(Chunk.GetData()), Chunk.Num());
        }
        if (Done.load()) return false;
        if (Cancelled.load()) { Fail(TEXT("cancelled"), TEXT("Request cancelled")); return false; }
    }
    if (Finished) {
        if (IsStream) { if (!State->Parser->finish()) Fail(Text(State->Parser->error), TEXT("Invalid UTF-8 stream")); else Fail(Success ? TEXT("incomplete_stream") : TEXT("transport_error"), TEXT("Connection ended before terminal event")); }
        else {
            const auto Object = TabForgeWire::Object(Parse(State->Body)); FTabForgeError EnvelopeError;
            if (!State->Envelope(Object, EnvelopeError)) { Fail(EnvelopeError.Code, EnvelopeError.Message); return false; }
            if (Object->HasField(TEXT("error"))) { if (Object->HasField(TEXT("data"))) { Fail(TEXT("invalid_frame"), TEXT("Response contains data and error")); return false; } const auto Remote = State->RemoteError(Object->TryGetField(TEXT("error"))); Fail(Remote.Code, Remote.Message, Remote.Retryable); return false; }
            if (!Success || Status < 200 || Status >= 300) { Fail(TEXT("http_error"), TEXT("Unexpected HTTP status")); return false; }
            TabForgeWire::Validator Validator(State->Schema); const auto Value = Object->TryGetField(TEXT("data"));
            if (!Validator.Validate(TabForgeWire::String(State->Operation, TEXT("responseType")), Value)) { Fail(TEXT("invalid_message"), Validator.Error); return false; }
            Complete(Value);
        }
        return false;
    }
    return true;
}

TSharedPtr<FTabForgeClient, ESPMode::ThreadSafe> FTabForgeClient::Create(const FString& URL, const FString& RuntimeJson, FTabForgeError& Error, FTabForgeOptions Options) {
    Error = {};
    if (!IsInGameThread() || !TabForgeWire::Matches(TEXT("^https?://[^/?#@]+(?:/[^?#]*)?$"), URL) || Options.MaxResponseBytes < 1 || Options.MaxFrameBytes < 1 || Options.MaxQueuedBytes < 1 || Options.MaxQueuedChunks < 1 || Options.MaxConcurrentRequests < 1) { Error = { TEXT("bad_request"), TEXT("Create on game thread with HTTP(S) URL and positive limits"), false }; return nullptr; }
    if (Bytes(RuntimeJson).size() > 1048576) { Error = { TEXT("response_too_large"), TEXT("Runtime metadata exceeds 1 MiB"), false }; return nullptr; }
    const auto Root = TabForgeWire::Object(Parse(Bytes(RuntimeJson)));
    if (!Root.IsValid() || TabForgeWire::String(Root, TEXT("version")).IsEmpty() || TabForgeWire::String(Root, TEXT("schemaHash")).IsEmpty() || !TabForgeWire::Object(Root->TryGetField(TEXT("operations"))).IsValid() || !TabForgeWire::Object(Root->TryGetField(TEXT("wireSchema"))).IsValid()) { Error = { TEXT("bad_request"), TEXT("Load generated runtime.json"), false }; return nullptr; }
    auto Client = TSharedPtr<FTabForgeClient, ESPMode::ThreadSafe>(new FTabForgeClient()); Client->BaseURL = URL; Client->BaseURL.RemoveFromEnd(TEXT("/")); Client->Contract = Root; Client->Options = Options;
    return Client;
}
FTabForgeClient::~FTabForgeClient() { CancelAll(); }
void FTabForgeClient::CancelAll() { for (const auto& Weak : Requests) if (auto Request = Weak.Pin()) Request->Cancel(); }
TSharedRef<FTabForgeRequest, ESPMode::ThreadSafe> FTabForgeClient::Send(const FString& Operation, const TSharedPtr<FJsonValue>& Data, FTabForgeCallbacks Callbacks, const FString& Token, const FString& Id, bool Streaming) {
    check(IsInGameThread());
    Requests.RemoveAll([](const auto& Weak) { auto Handle = Weak.Pin(); return !Handle.IsValid() || Handle->IsDone(); });
    const bool OverLimit = Requests.Num() >= Options.MaxConcurrentRequests;
    const auto Request = MakeShared<FTabForgeRequest, ESPMode::ThreadSafe>(); Requests.Add(Request);
    Request->Begin(BaseURL, Contract, Operation, Data, MoveTemp(Callbacks), Token, Id, Streaming, Options, OverLimit); return Request;
}
TSharedRef<FTabForgeRequest, ESPMode::ThreadSafe> FTabForgeClient::Call(const FString& Operation, const TSharedPtr<FJsonValue>& Data, FTabForgeCallbacks Callbacks, const FString& Token, const FString& Id) { return Send(Operation, Data, MoveTemp(Callbacks), Token, Id, false); }
TSharedRef<FTabForgeRequest, ESPMode::ThreadSafe> FTabForgeClient::Stream(const FString& Operation, const TSharedPtr<FJsonValue>& Data, FTabForgeCallbacks Callbacks, const FString& Token, const FString& Id) { return Send(Operation, Data, MoveTemp(Callbacks), Token, Id, true); }

#include "TabForgeRealtime.h"
#include "TabForgeRealtimeCore.h"
#include "TabForgeKcp.h"
#include "SocketSubsystem.h"
#include "Sockets.h"
#include <random>
#include "TabForgeWire.h"
#include "Containers/Ticker.h"
#include "IWebSocket.h"
#include "WebSocketsModule.h"

namespace TabForgeRealtimeDetail {
namespace RT = tabforge::realtime;

std::string Utf8(const FString& Text) { FTCHARToUTF8 Converted(*Text); return std::string(Converted.Get(), Converted.Length()); }
FString FromUtf8(const std::string& Text) { return TabForgeWire::Utf8Text(Text); }
FTabForgeError ToError(const RT::Error& E) { return { FromUtf8(E.code), FromUtf8(E.message), E.retryable }; }

ETabForgeRealtimeState ToState(RT::State S) {
    switch (S) {
    case RT::State::Idle: return ETabForgeRealtimeState::Idle;
    case RT::State::Connecting: return ETabForgeRealtimeState::Connecting;
    case RT::State::Connected: return ETabForgeRealtimeState::Connected;
    case RT::State::Resumed: return ETabForgeRealtimeState::Resumed;
    case RT::State::Disconnected: return ETabForgeRealtimeState::Disconnected;
    case RT::State::Closed: return ETabForgeRealtimeState::Closed;
    }
    return ETabForgeRealtimeState::Closed;
}

// Canonical ProtoJSON FJsonValue -> the core's JSON tree (strings stay UTF-8).
RT::Json ToCore(const TSharedPtr<FJsonValue>& Value) {
    if (!Value.IsValid()) return RT::Json();
    switch (Value->Type) {
    case EJson::Boolean: return RT::Json::from_bool(Value->AsBool());
    case EJson::Number: return RT::Json::from_double(Value->AsNumber());
    case EJson::String: return RT::Json::from_string(Utf8(Value->AsString()));
    case EJson::Array: {
        RT::Json Out = RT::Json::array();
        for (const auto& Item : Value->AsArray()) Out.items.push_back(ToCore(Item));
        return Out;
    }
    case EJson::Object: {
        RT::Json Out = RT::Json::object();
        for (const auto& Pair : Value->AsObject()->Values) Out.members.emplace_back(Utf8(Pair.Key), ToCore(Pair.Value));
        return Out;
    }
    default: return RT::Json();
    }
}

// Decoded message -> FJsonValue. FJsonObject compares member names without
// case, so names that differ only by case (possible in string-keyed maps)
// fail instead of being merged.
bool FromCore(const RT::Json& Value, TSharedPtr<FJsonValue>& Out) {
    switch (Value.type) {
    case RT::Json::Type::Null: Out = MakeShared<FJsonValueNull>(); return true;
    case RT::Json::Type::Bool: Out = MakeShared<FJsonValueBoolean>(Value.boolean); return true;
    case RT::Json::Type::Number: Out = MakeShared<FJsonValueNumber>(Value.number); return true;
    case RT::Json::Type::String: Out = MakeShared<FJsonValueString>(FromUtf8(Value.text)); return true;
    case RT::Json::Type::Array: {
        TArray<TSharedPtr<FJsonValue>> Items;
        for (const RT::Json& Item : Value.items) {
            TSharedPtr<FJsonValue> Converted;
            if (!FromCore(Item, Converted)) return false;
            Items.Add(Converted);
        }
        Out = MakeShared<FJsonValueArray>(Items);
        return true;
    }
    case RT::Json::Type::Object: {
        auto Object = MakeShared<FJsonObject>();
        for (const auto& Member : Value.members) {
            const FString Key = FromUtf8(Member.first);
            if (Object->Values.Contains(Key)) return false;
            TSharedPtr<FJsonValue> Converted;
            if (!FromCore(Member.second, Converted)) return false;
            Object->SetField(Key, Converted);
        }
        Out = MakeShared<FJsonValueObject>(Object);
        return true;
    }
    }
    return false;
}
} // namespace TabForgeRealtimeDetail

using namespace TabForgeRealtimeDetail;

struct FTabForgeRealtimeClient::FState : public TSharedFromThis<FState, ESPMode::ThreadSafe>, public RT::Socket {
    static constexpr SIZE_T MaxMessageBytes = 1 << 20;

    FString Url;
    TMap<FString, FString> UpgradeHeaders;
    TSharedPtr<FJsonObject> WireSchema;
    RT::Codec Codec;
    TUniquePtr<RT::Client> Core;
    TMap<uint64, TSharedPtr<IWebSocket>> Sockets;
    uint64 NextSocket = 1;
    TMap<FString, TArray<TFunction<void(const TSharedPtr<FJsonObject>&)>>> Handlers;
    FTSTicker::FDelegateHandle Ticker;
    FTabForgeRealtimeClient* Owner = nullptr;

    // KCP (kcp:// URLs): one UDP socket and conversation per connection
    // attempt, polled from Tick on the game thread. The protocol, crypto and
    // handshake are the engine-independent TabForgeKcp.h.
    bool bKcp = false;
    FString KcpHost;
    int32 KcpPort = 0;
    std::string KcpPath;
    tabforge::kcp::Bytes KcpServerKey;
    struct FKcpSocket {
        FSocket* Socket = nullptr;
        TSharedPtr<FInternetAddr> Address;
        std::unique_ptr<tabforge::kcp::Conversation> Conversation;
    };
    TMap<uint64, TUniquePtr<FKcpSocket>> KcpSockets;
    std::random_device Random;

    ~FState() {
        TArray<uint64> Ids;
        KcpSockets.GetKeys(Ids);
        for (uint64 Id : Ids) DestroyKcp(Id);
    }

    static int64 NowMS() { return static_cast<int64>(FPlatformTime::Seconds() * 1000.0); }

    uint64 OpenKcp(const std::string& Subprotocol) {
        ISocketSubsystem* Subsystem = ISocketSubsystem::Get(PLATFORM_SOCKETSUBSYSTEM);
        if (!Subsystem) return 0;
        // Resolves on the game thread; an IP address avoids a DNS stall.
        FAddressInfoResult Resolved = Subsystem->GetAddressInfo(*KcpHost, nullptr, EAddressInfoFlags::Default, NAME_None, ESocketType::SOCKTYPE_Datagram);
        if (Resolved.ReturnCode != SE_NO_ERROR || Resolved.Results.Num() == 0) return 0;
        TSharedRef<FInternetAddr> Address = Resolved.Results[0].Address;
        Address->SetPort(KcpPort);
        FSocket* Socket = Subsystem->CreateSocket(NAME_DGram, TEXT("TabForge KCP"), Address->GetProtocolType());
        if (!Socket) return 0;
        Socket->SetNonBlocking(true);
        int32 BufferSize = 0;
        Socket->SetReceiveBufferSize(1 << 20, BufferSize);
        const uint64 Id = NextSocket++;
        auto Entry = MakeUnique<FKcpSocket>();
        Entry->Socket = Socket;
        Entry->Address = Address;
        // Conversation events fire inside PollKcp/send/close on the game
        // thread and are queued by the core like WebSocket events.
        tabforge::kcp::Conversation::Events Events;
        Events.opened = [this, Id](const std::string& Protocol) { Core->opened(Id, Protocol); };
        Events.received = [this, Id](std::string Data, bool bText) { Core->received(Id, std::move(Data), bText); };
        Events.refused = [this, Id](const std::string& Message) { Core->refused(Id, Message); };
        Events.closed = [this, Id](const std::string& Reason) { Core->closed(Id, Reason); };
        uint32 Conv = 0;
        while (Conv == 0) Conv = Random();
        tabforge::kcp::crypto::Key Ephemeral{};
        for (auto& Byte : Ephemeral) Byte = static_cast<uint8>(Random());
        FSocket* Raw = Socket;
        TSharedPtr<FInternetAddr> To = Address;
        Entry->Conversation = std::make_unique<tabforge::kcp::Conversation>(Conv, KcpPath, Subprotocol, KcpServerKey, Ephemeral,
            [Raw, To](const std::uint8_t* Data, std::size_t Size) {
                int32 Sent = 0;
                Raw->SendTo(Data, static_cast<int32>(Size), Sent, *To); // a failed send is a lost datagram
            }, Events);
        tabforge::kcp::Conversation* Conversation = Entry->Conversation.get();
        KcpSockets.Add(Id, MoveTemp(Entry));
        Conversation->start(NowMS());
        return Id;
    }

    void DestroyKcp(uint64 Id) {
        TUniquePtr<FKcpSocket>* Found = KcpSockets.Find(Id);
        if (!Found) return;
        TUniquePtr<FKcpSocket> Entry = MoveTemp(*Found); // TUniquePtr cannot be copied out
        KcpSockets.Remove(Id);
        Entry->Socket->Close();
        if (ISocketSubsystem* Subsystem = ISocketSubsystem::Get(PLATFORM_SOCKETSUBSYSTEM)) Subsystem->DestroySocket(Entry->Socket);
    }

    void PollKcp() {
        const int64 Now = NowMS();
        static uint8 Buffer[64 << 10];
        ISocketSubsystem* Subsystem = ISocketSubsystem::Get(PLATFORM_SOCKETSUBSYSTEM);
        TArray<uint64> Ids;
        KcpSockets.GetKeys(Ids);
        for (uint64 Id : Ids) {
            const TUniquePtr<FKcpSocket>* Found = KcpSockets.Find(Id);
            if (!Found) continue;
            FKcpSocket* Entry = Found->Get();
            TSharedRef<FInternetAddr> From = Subsystem->CreateInternetAddr();
            uint32 Pending = 0;
            while (!Entry->Conversation->closed() && Entry->Socket->HasPendingData(Pending)) {
                int32 Read = 0;
                if (!Entry->Socket->RecvFrom(Buffer, sizeof Buffer, Read, *From)) break;
                if (*From == *Entry->Address) Entry->Conversation->input(Buffer, static_cast<std::size_t>(Read), Now);
            }
            Entry->Conversation->update(Now);
            if (Entry->Conversation->closed()) DestroyKcp(Id);
        }
    }

    // Validates and normalizes a message, then encodes it as binary Protobuf.
    bool Encode(const RT::Spec& Spec, const TSharedPtr<FJsonValue>& Message, std::string& Payload, FTabForgeError& Error) {
        TabForgeWire::Validator Normalizer(WireSchema, TabForgeWire::JsonMaxDepth, TabForgeWire::ProtocolMaxNodes, true);
        if (!Normalizer.Validate(FromUtf8(Spec.type), TabForgeWire::Clone(Message))) {
            Error = { TEXT("invalid_message"), Normalizer.Error, false };
            return false;
        }
        std::string Why;
        if (!Codec.encode(Spec.type, ToCore(Normalizer.Result), Payload, Why)) {
            Error = { TEXT("invalid_message"), FromUtf8(Why), false };
            return false;
        }
        return true;
    }

    bool Decode(const std::string& Type, const std::string& Payload, TSharedPtr<FJsonObject>& Out, FTabForgeError& Error) {
        RT::Json Decoded;
        std::string Why;
        TSharedPtr<FJsonValue> Value;
        if (!Codec.decode(Type, Payload, Decoded, Why)) { Error = { TEXT("protocol_error"), TEXT("Invalid message: ") + FromUtf8(Why), false }; return false; }
        if (!FromCore(Decoded, Value)) { Error = { TEXT("protocol_error"), TEXT("Message members differ only by case"), false }; return false; }
        TabForgeWire::Validator Validator(WireSchema);
        if (!Validator.Validate(FromUtf8(Type), Value)) { Error = { TEXT("protocol_error"), TEXT("Invalid message: ") + Validator.Error, false }; return false; }
        Out = Value->AsObject();
        return true;
    }

    // ---- RT::Socket on IWebSocket ----

    uint64 open(const std::string& Subprotocol) override {
        if (bKcp) return OpenKcp(Subprotocol);
        const uint64 Id = NextSocket++;
        TSharedPtr<IWebSocket> Socket = FWebSocketsModule::Get().CreateWebSocket(Url, FromUtf8(Subprotocol), UpgradeHeaders);
        TWeakPtr<FState, ESPMode::ThreadSafe> Weak = AsShared();
        // Engine socket events are queued in the core (thread safe) and handled in Tick.
        Socket->OnConnected().AddLambda([Weak, Id]() {
            if (auto Self = Weak.Pin()) Self->Core->opened(Id, ""); // IWebSocket does not expose the accepted subprotocol
        });
        Socket->OnConnectionError().AddLambda([Weak, Id](const FString&) {
            if (auto Self = Weak.Pin()) Self->Core->closed(Id, "");
        });
        Socket->OnClosed().AddLambda([Weak, Id](int32 StatusCode, const FString& Reason, bool) {
            // The server writes its error code as the reason of a normal close.
            if (auto Self = Weak.Pin()) Self->Core->closed(Id, StatusCode == 1000 ? Utf8(Reason) : std::string());
        });
        Socket->OnMessage().AddLambda([Weak, Id](const FString& Text) {
            if (auto Self = Weak.Pin()) Self->Core->received(Id, Utf8(Text), true); // a protocol error on the binary subprotocol
        });
        auto Buffer = MakeShared<std::string, ESPMode::ThreadSafe>();
        auto Oversized = MakeShared<bool, ESPMode::ThreadSafe>(false);
        Socket->OnBinaryMessage().AddLambda([Weak, Id, Buffer, Oversized](const void* Data, SIZE_T Size, bool bIsLastFragment) {
            auto Self = Weak.Pin();
            if (!Self) return;
            if (!*Oversized && Buffer->size() + Size > MaxMessageBytes) { *Oversized = true; Buffer->clear(); Self->Core->closed(Id, ""); }
            if (!*Oversized) Buffer->append(static_cast<const char*>(Data), Size);
            if (!bIsLastFragment) return;
            if (!*Oversized) Self->Core->received(Id, MoveTemp(*Buffer), false);
            Buffer->clear();
            *Oversized = false;
        });
        Sockets.Add(Id, Socket);
        Socket->Connect();
        return Id;
    }

    bool send(uint64 Id, const std::string& Frame) override {
        if (bKcp) {
            const TUniquePtr<FKcpSocket>* Entry = KcpSockets.Find(Id);
            return Entry && (*Entry)->Conversation->send(Frame, false);
        }
        const TSharedPtr<IWebSocket>* Socket = Sockets.Find(Id);
        if (!Socket || !(*Socket)->IsConnected()) return false;
        (*Socket)->Send(Frame.data(), Frame.size(), true);
        return true;
    }

    void close(uint64 Id) override {
        if (bKcp) {
            // Sends the close message; PollKcp destroys the socket once it is
            // acknowledged or after a second. Its events still reach the
            // core, which ignores a connection it has dropped.
            if (const TUniquePtr<FKcpSocket>* Entry = KcpSockets.Find(Id)) (*Entry)->Conversation->close(NowMS());
            return;
        }
        TSharedPtr<IWebSocket> Socket;
        if (!Sockets.RemoveAndCopyValue(Id, Socket)) return;
        // Stop delivering events, then close after what is already queued.
        Socket->OnConnected().Clear();
        Socket->OnConnectionError().Clear();
        Socket->OnMessage().Clear();
        Socket->OnBinaryMessage().Clear();
        Socket->OnClosed().Clear();
        if (Socket->IsConnected()) Socket->Close(1000, TEXT(""));
    }

    bool Tick() {
        if (bKcp) PollKcp();
        Core->tick();
        return true;
    }
};

TSharedPtr<FTabForgeRealtimeClient, ESPMode::ThreadSafe> FTabForgeRealtimeClient::Create(const FString& RuntimeJson, FTabForgeRealtimeOptions Options, FTabForgeError& Error) {
    check(IsInGameThread());
    const bool bKcp = Options.Url.StartsWith(TEXT("kcp://"));
    std::string KcpHost, KcpPath;
    int KcpPort = 0;
    if ((bKcp ? !tabforge::kcp::parse_url(Utf8(Options.Url), KcpHost, KcpPort, KcpPath) : !TabForgeWire::Matches(TEXT("^wss?://[^/?#@]+(?:/[^?#]*)?$"), Options.Url)) || Options.MinBackoffMS < 1 || Options.MaxBackoffMS < Options.MinBackoffMS || Options.MaxUnacked < 1) {
        Error = { TEXT("bad_request"), TEXT("Create on the game thread with a ws(s):// or kcp://host:port URL and positive limits"), false };
        return nullptr;
    }
    if (bKcp && Options.KcpServerKey.Num() != 0 && Options.KcpServerKey.Num() != 32) {
        Error = { TEXT("bad_request"), TEXT("KcpServerKey is the server's 32-byte X25519 public key"), false };
        return nullptr;
    }
    const std::string Text = Utf8(RuntimeJson);
    TabForgeWire::FJsonDocument Document;
    RT::Json Runtime;
    if (!TabForgeWire::Parse(Text, Document, TabForgeWire::JsonMaxDepth, 10000000) || !tabforge::realtime::parse_json(Text, Runtime, 10000000)) {
        Error = { TEXT("bad_request"), TEXT("Load the generated runtime.json"), false };
        return nullptr;
    }
    TSharedPtr<FJsonObject> Root = TabForgeWire::Object(Document.Root);
    TSharedPtr<FJsonObject> WireSchema = Root.IsValid() ? TabForgeWire::Object(Root->TryGetField(TEXT("wireSchema"))) : nullptr;
    const RT::Json* CodecSchema = Runtime.find("codecSchema");
    if (!WireSchema.IsValid() || !CodecSchema) {
        Error = { TEXT("bad_request"), TEXT("runtime.json has no wireSchema/codecSchema; regenerate it with channels"), false };
        return nullptr;
    }
    TSharedPtr<FTabForgeRealtimeClient, ESPMode::ThreadSafe> Client(new FTabForgeRealtimeClient());
    auto State = MakeShared<FState, ESPMode::ThreadSafe>();
    State->Url = Options.Url;
    State->UpgradeHeaders = Options.UpgradeHeaders;
    State->bKcp = bKcp;
    State->KcpHost = FromUtf8(KcpHost);
    State->KcpPort = KcpPort;
    State->KcpPath = KcpPath;
    State->KcpServerKey.assign(Options.KcpServerKey.GetData(), Options.KcpServerKey.GetData() + Options.KcpServerKey.Num());
    State->WireSchema = WireSchema;
    State->Owner = Client.Get();
    std::string Why;
    if (!State->Codec.load(*CodecSchema, Why)) { Error = { TEXT("bad_request"), FromUtf8(Why), false }; return nullptr; }
    State->Core = MakeUnique<RT::Client>(*State);
    RT::ClientOptions CoreOptions;
    CoreOptions.min_backoff_ms = Options.MinBackoffMS;
    CoreOptions.max_backoff_ms = Options.MaxBackoffMS;
    CoreOptions.max_unacked = static_cast<std::size_t>(Options.MaxUnacked);
    if (!State->Core->configure(Runtime, Utf8(Options.Channel), CoreOptions, Why)) { Error = { TEXT("bad_request"), FromUtf8(Why), false }; return nullptr; }
    if (Options.Token) {
        TFunction<FString()> Token = MoveTemp(Options.Token);
        State->Core->token = [Token]() { return Utf8(Token()); };
    }
    FState* Raw = &State.Get();
    State->Core->on_state = [Raw](RT::State Next, const RT::Error* Failure) {
        FTabForgeRealtimeClient* Owner = Raw->Owner;
        if (!Owner || !Owner->OnState) return;
        if (Failure) { const FTabForgeError E = ToError(*Failure); Owner->OnState(ToState(Next), &E); }
        else Owner->OnState(ToState(Next), nullptr);
    };
    State->Core->on_push = [Raw](const RT::Spec& Spec, const std::string& Payload) {
        FTabForgeRealtimeClient* Owner = Raw->Owner;
        const auto* List = Raw->Handlers.Find(FromUtf8(Spec.name));
        if (!Owner || !List || List->Num() == 0) return;
        TSharedPtr<FJsonObject> Message;
        FTabForgeError Failure;
        if (!Raw->Decode(Spec.type, Payload, Message, Failure)) {
            if (Owner->OnHandlerError) Owner->OnHandlerError(Failure);
            return;
        }
        const auto Copy = *List; // handlers may subscribe while running
        for (const auto& Handler : Copy) Handler(Message);
    };
    TWeakPtr<FState, ESPMode::ThreadSafe> Weak = State;
    State->Ticker = FTSTicker::GetCoreTicker().AddTicker(FTickerDelegate::CreateLambda([Weak](float) {
        if (auto Self = Weak.Pin()) return Self->Tick();
        return false;
    }));
    Client->State = State;
    return Client;
}

FTabForgeRealtimeClient::~FTabForgeRealtimeClient() {
    if (!State.IsValid()) return;
    State->Owner = nullptr; // no callbacks into a destroyed client
    FTSTicker::GetCoreTicker().RemoveTicker(State->Ticker);
    State->Core->close();
    TArray<uint64> Ids;
    State->Sockets.GetKeys(Ids);
    for (uint64 Id : Ids) State->close(Id);
    // KCP: close() sends the close messages now; the sockets go with the
    // state (~FState), since nothing ticks them any more.
    State->KcpSockets.GetKeys(Ids);
    for (uint64 Id : Ids) State->close(Id);
}

bool FTabForgeRealtimeClient::Connect(FTabForgeError& Error) {
    check(IsInGameThread());
    RT::Error Failure;
    if (State->Core->connect(Failure)) return true;
    Error = ToError(Failure);
    return false;
}

bool FTabForgeRealtimeClient::Request(const FString& Name, const TSharedPtr<FJsonValue>& Message, FTabForgeRealtimeCallbacks Callbacks, FTabForgeError& Error, int32 TimeoutMS) {
    check(IsInGameThread());
    const RT::Spec* Spec = State->Core->spec(Utf8(Name));
    if (!Spec || Spec->kind != "request") { Error = { TEXT("invalid_message"), TEXT("No request message ") + Name, false }; return false; }
    std::string Payload;
    if (!State->Encode(*Spec, Message, Payload, Error)) return false;
    FState* Raw = &State.Get();
    const std::string ResponseType = Spec->response_type;
    auto Done = [Raw, ResponseType, Callbacks = MoveTemp(Callbacks)](const RT::Reply& Reply) {
        if (!Raw->Owner) return;
        if (!Reply.ok) { if (Callbacks.OnError) Callbacks.OnError(ToError(Reply.error)); return; }
        TSharedPtr<FJsonObject> Response;
        FTabForgeError Failure;
        if (!Raw->Decode(ResponseType, Reply.payload, Response, Failure)) { if (Callbacks.OnError) Callbacks.OnError(Failure); return; }
        if (Callbacks.OnResponse) Callbacks.OnResponse(Response);
    };
    RT::Error Failure;
    if (State->Core->request(Spec->name, MoveTemp(Payload), TimeoutMS > 0 ? TimeoutMS : 0, MoveTemp(Done), Failure)) return true;
    Error = ToError(Failure);
    return false;
}

bool FTabForgeRealtimeClient::Notify(const FString& Name, const TSharedPtr<FJsonValue>& Message, FTabForgeError& Error) {
    check(IsInGameThread());
    const RT::Spec* Spec = State->Core->spec(Utf8(Name));
    if (!Spec || Spec->kind != "notify") { Error = { TEXT("invalid_message"), TEXT("No notify message ") + Name, false }; return false; }
    std::string Payload;
    if (!State->Encode(*Spec, Message, Payload, Error)) return false;
    RT::Error Failure;
    if (State->Core->notify(Spec->name, MoveTemp(Payload), Failure)) return true;
    Error = ToError(Failure);
    return false;
}

bool FTabForgeRealtimeClient::OnPush(const FString& Name, TFunction<void(const TSharedPtr<FJsonObject>&)> Handler, FTabForgeError& Error) {
    check(IsInGameThread());
    const RT::Spec* Spec = State->Core->spec(Utf8(Name));
    if (!Spec || Spec->kind != "push" || !Handler) { Error = { TEXT("invalid_message"), TEXT("No push message ") + Name, false }; return false; }
    State->Handlers.FindOrAdd(Name).Add(MoveTemp(Handler));
    return true;
}

void FTabForgeRealtimeClient::Close() {
    check(IsInGameThread());
    State->Core->close();
}

ETabForgeRealtimeState FTabForgeRealtimeClient::GetState() const { return ToState(State->Core->state()); }

FString FTabForgeRealtimeClient::GetSessionId() const { return FromUtf8(State->Core->session_id()); }

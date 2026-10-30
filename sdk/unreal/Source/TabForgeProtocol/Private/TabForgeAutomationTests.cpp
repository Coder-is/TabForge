#if WITH_DEV_AUTOMATION_TESTS
#include "TabForgeClient.h"
#include "TabForgeSse.h"
#include "TabForgeJsonSyntax.h"
#include "TabForgeWire.h"
#include "Misc/AutomationTest.h"
#include "Misc/FileHelper.h"
#include "HAL/PlatformMisc.h"
#include "HAL/PlatformTime.h"
#include "Serialization/JsonReader.h"
#include "Serialization/JsonSerializer.h"

IMPLEMENT_SIMPLE_AUTOMATION_TEST(FTabForgeParserTest, "TabForge.Protocol.Parser", EAutomationTestFlags::EditorContext | EAutomationTestFlags::EngineFilter)
bool FTabForgeParserTest::RunTest(const FString&) {
    const std::string Input = "\xef\xbb\xbf: heartbeat\r\n\r\nid: 1\r\nevent: text.delta\r\ndata: {\"text\":\"\xe4\xbd\xa0\xf0\x9f\x98\x80\"}\r\n\r\n";
    for (std::size_t Split = 0; Split <= Input.size(); ++Split) {
        tabforge::SseParser Parser; int Count = 0;
        auto Receive = [&](const tabforge::SseFrame& Frame) { ++Count; return Frame.id == "1" && Frame.event == "text.delta" && tabforge::JsonSyntax(Frame.data).valid(); };
        TestTrue(TEXT("First fragment"), Parser.feed(reinterpret_cast<const std::uint8_t*>(Input.data()), Split, Receive));
        TestTrue(TEXT("Second fragment"), Parser.feed(reinterpret_cast<const std::uint8_t*>(Input.data()) + Split, Input.size() - Split, Receive));
        TestTrue(TEXT("Finish"), Parser.finish()); TestEqual(TEXT("One frame"), Count, 1);
    }
    TestFalse(TEXT("Duplicate keys rejected"), tabforge::JsonSyntax("{\"x\":1,\"\\u0078\":2}").valid());
    return true;
}

IMPLEMENT_SIMPLE_AUTOMATION_TEST(FTabForgeWireTest, "TabForge.Protocol.Wire", EAutomationTestFlags::EditorContext | EAutomationTestFlags::EngineFilter)
bool FTabForgeWireTest::RunTest(const FString&) {
    // Mirrors examples/backend/cases.json: Go protojson input rules.
    TabForgeWire::FJsonDocument SchemaDocument;
    const std::string SchemaText = R"({"messages":{"M":{"fields":{"i":{"kind":"int32"},"u":{"kind":"uint32"},"l":{"kind":"int64"},"q":{"kind":"uint64"},"e":{"kind":"enum","type":"E"},"b":{"kind":"bytes"},"d":{"kind":"double"},"n":{"kind":"int32","list":true},"m":{"kind":"int32","mapKey":"int32"},"s":{"kind":"message","type":"google.protobuf.Struct"},"x":{"kind":"string"},"y":{"kind":"int32"},"next":{"kind":"message","type":"M"}},"oneofs":[["x","y"]]},"google.protobuf.Struct":{"fields":{}}},"enums":{"E":["ZERO","ONE"]}})";
    if (!TestTrue(TEXT("Schema parses"), TabForgeWire::Parse(SchemaText, SchemaDocument))) return false;
    const auto Schema = SchemaDocument.Root->AsObject();
    auto Check = [&](const std::string& Json, bool Expected) {
        TabForgeWire::FJsonDocument Document;
        const bool Parsed = TabForgeWire::Parse(Json, Document, TabForgeWire::JsonMaxDepth, TabForgeWire::DataMaxNodes);
        TabForgeWire::Validator Validator(Schema, TabForgeWire::JsonMaxDepth, TabForgeWire::DataMaxNodes, true);
        const bool Valid = Parsed && Validator.Validate(TEXT("M"), Document.Root, &Document.Numbers);
        TestEqual(FString(UTF8_TO_TCHAR(Json.c_str())), Valid, Expected);
        return Valid ? TabForgeWire::Object(Validator.Result) : TSharedPtr<FJsonObject>();
    };
    for (const char* Json : { R"({"i":1.0})", R"({"i":1e2})", R"({"i":100e-2})", R"({"i":-0})", R"({"i":"1e2"})", R"({"u":4294967295.0})", R"({"q":18446744073709551615})", R"({"q":"1.8446744073709551615e19"})", R"({"l":9007199254740993})", R"({"e":1.0})", R"({"e":"ONE"})", R"({"b":"AA-_"})", R"({"b":"AAF="})", R"({"m":{"+1":1,"02":2}})", R"({"d":"1.5"})", R"({"d":"NaN"})", R"({"x":null,"y":1})", R"({"s":{"a":null}})", R"({"n":null})" })
        Check(Json, true);
    for (const char* Json : { R"({"i":1.5})", R"({"i":1e-2})", R"({"i":2147483648})", R"({"i":" 1"})", R"({"i":"01"})", R"({"i":1.0000000000000001})", R"({"i":0.000000000000000000001e21})", R"({"u":-1})", R"({"q":18446744073709551616})", R"({"e":"1"})", R"({"e":"one"})", R"({"b":"AA+_"})", R"({"b":"AA="})", R"({"m":{"1":1,"01":2}})", R"({"m":{"1e0":1}})", R"({"d":" 1"})", R"({"n":[null]})", R"({"x":"a","y":1})", R"({"I":1})", R"({"i":1,"I":2})" })
        Check(Json, false);
    const auto Normalized = Check(R"({"i":"1e2","q":18446744073709551615,"l":-9.223372036854775808e18,"e":1.0,"b":"AAF","n":[1.0,"2"],"m":{"+1":3},"x":null})", true);
    if (Normalized.IsValid()) {
        TestEqual(TEXT("int32 number"), Normalized->GetNumberField(TEXT("i")), 100.0);
        TestEqual(TEXT("uint64 exact string"), Normalized->GetStringField(TEXT("q")), FString(TEXT("18446744073709551615")));
        TestEqual(TEXT("int64 exact string"), Normalized->GetStringField(TEXT("l")), FString(TEXT("-9223372036854775808")));
        TestEqual(TEXT("canonical bytes"), Normalized->GetStringField(TEXT("b")), FString(TEXT("AAE=")));
        TestTrue(TEXT("canonical map key"), Normalized->GetObjectField(TEXT("m"))->HasField(TEXT("1")));
        TestFalse(TEXT("null member removed"), Normalized->HasField(TEXT("x")));
    }
    // Depth equals JSON depth: a value at depth 64 is accepted, 65 rejected.
    auto Nest = [](int32 Levels) { std::string Json = "{\"i\":1}"; for (int32 I = 0; I < Levels; ++I) Json = "{\"next\":" + Json + "}"; return Json; };
    Check(Nest(63), true);
    Check(Nest(64), false);
    return true;
}

namespace {
struct FCase {
    FString Name, Error, URL, Runtime;
    bool Stream = true, Cancel = false, CancelAll = false;
    int Events = 0;
    FString FirstText, LastName, LastSequence;
    TSharedPtr<FJsonValue> Result;
    FTabForgeError Failure;
    TSharedPtr<FTabForgeClient, ESPMode::ThreadSafe> Client;
    TSharedPtr<FTabForgeRequest, ESPMode::ThreadSafe> Request;
};
class FHttpSmokeCommand : public IAutomationLatentCommand {
    FAutomationTestBase* Test;
    FString URL, Runtime;
    TArray<FString> Names { TEXT("unary"), TEXT("stream"), TEXT("cancel"), TEXT("timeout"), TEXT("gap"), TEXT("schema"), TEXT("type"), TEXT("eof"), TEXT("large"), TEXT("utf8"), TEXT("cancelall") };
    int32 Index = 0;
    TSharedPtr<FCase> Active;
    double Deadline = 0;
public:
    FHttpSmokeCommand(FAutomationTestBase* InTest, FString InURL, FString InRuntime) : Test(InTest), URL(MoveTemp(InURL)), Runtime(MoveTemp(InRuntime)) {}
    bool Update() override {
        if (Active.IsValid()) {
            if (!Active->Request->IsDone()) {
                if (FPlatformTime::Seconds() < Deadline) return false;
                Active->Request->Cancel(); Test->AddError(TEXT("Native HTTP acceptance deadline exceeded: ") + Active->Name); return true;
            }
            Test->TestEqual(Active->Name + TEXT(" error"), Active->Failure.Code, Active->Error);
            if (Active->Error.IsEmpty()) {
                if (Active->Stream) {
                    Test->TestEqual(TEXT("Incremental event count"), Active->Events, 2);
                    Test->TestEqual(TEXT("Unicode"), Active->FirstText, FString(TEXT("你好😀")));
                    Test->TestEqual(TEXT("Terminal"), Active->LastName, FString(TEXT("completed")));
                    Test->TestEqual(TEXT("Sequence"), Active->LastSequence, FString(TEXT("2")));
                } else {
                    Test->TestTrue(TEXT("Unary object"), Active->Result.IsValid() && Active->Result->Type == EJson::Object);
                    if (Active->Result.IsValid() && Active->Result->Type == EJson::Object) {
                        auto Data = Active->Result->AsObject();
                        Test->TestEqual(TEXT("Unicode"), Data->GetStringField(TEXT("text")), FString(TEXT("你好😀")));
                        Test->TestEqual(TEXT("uint64 string"), Data->GetObjectField(TEXT("usage"))->GetStringField(TEXT("outputTokens")), FString(TEXT("18446744073709551615")));
                    }
                }
            }
            if (Active->Cancel) Test->TestEqual(TEXT("Event arrived before cancellation/EOF"), Active->Events, 1);
            Active.Reset(); ++Index;
        }
        if (Index == Names.Num()) return true;
        Active = MakeShared<FCase>(); Active->Name = Names[Index]; Active->Stream = Active->Name != TEXT("unary");
        Active->Cancel = Active->Name == TEXT("cancel"); Active->CancelAll = Active->Name == TEXT("cancelall");
        TSharedPtr<FJsonObject> Root; FJsonSerializer::Deserialize(TJsonReaderFactory<>::Create(Runtime), Root);
        if (!Root.IsValid()) { Test->AddError(TEXT("Invalid runtime metadata")); return true; }
        auto Operation = Root->GetObjectField(TEXT("operations"))->GetObjectField(TEXT("chatStream"));
        if (Active->Name == TEXT("timeout")) { Operation->SetNumberField(TEXT("timeoutMS"), 150); Active->Error = TEXT("timeout"); }
        else if (Active->Cancel || Active->CancelAll) Active->Error = TEXT("cancelled");
        else if (Active->Name != TEXT("unary") && Active->Name != TEXT("stream")) {
            Operation->SetStringField(TEXT("path"), TEXT("/_platform/fault/") + Active->Name);
            const TMap<FString, FString> Codes { {TEXT("gap"), TEXT("invalid_sequence")}, {TEXT("schema"), TEXT("schema_mismatch")}, {TEXT("type"), TEXT("invalid_message")}, {TEXT("eof"), TEXT("incomplete_stream")}, {TEXT("large"), TEXT("frame_too_large")}, {TEXT("utf8"), TEXT("invalid_utf8")} };
            Active->Error = Codes[Active->Name];
        }
        FString Metadata; FJsonSerializer::Serialize(Root.ToSharedRef(), TJsonWriterFactory<>::Create(&Metadata));
        FTabForgeOptions Options; Options.MaxQueuedBytes = 4 * 1048576; Options.MaxQueuedChunks = 1024;
        FTabForgeError Startup; Active->Client = FTabForgeClient::Create(URL, Metadata, Startup, Options);
        if (!Active->Client.IsValid()) { Test->AddError(Startup.Code + TEXT(": ") + Startup.Message); return true; }
        auto Data = MakeShared<FJsonObject>(); Data->SetStringField(TEXT("conversationId"), TEXT("18446744073709551615"));
        Data->SetStringField(TEXT("prompt"), (Active->Cancel || Active->CancelAll) ? TEXT("cancel") : Active->Name == TEXT("timeout") ? TEXT("timeout") : TEXT("你好😀"));
        const TWeakPtr<FCase> Weak = Active;
        FTabForgeCallbacks Callbacks;
        Callbacks.OnError = [Weak](const FTabForgeError& Error) { if (auto State = Weak.Pin()) State->Failure = Error; };
        Callbacks.OnComplete = [Weak](const TSharedPtr<FJsonValue>& Value) { if (auto State = Weak.Pin()) State->Result = Value; };
        Callbacks.OnEvent = [Weak](const FTabForgeEvent& Event) {
            if (auto State = Weak.Pin()) {
                ++State->Events; State->LastName = Event.Name; State->LastSequence = Event.Sequence;
                if (Event.Name == TEXT("text.delta")) State->FirstText = Event.Payload->GetObjectField(TEXT("delta"))->GetStringField(TEXT("text"));
                if (State->Cancel) State->Request->Cancel();
            }
        };
        auto Value = MakeShared<FJsonValueObject>(Data);
        Active->Request = Active->Stream ? Active->Client->Stream(TEXT("chatStream"), Value, MoveTemp(Callbacks)) : Active->Client->Call(TEXT("chatComplete"), Value, MoveTemp(Callbacks));
        if (Active->CancelAll) Active->Client->CancelAll();
        Deadline = FPlatformTime::Seconds() + 10; return false;
    }
};
}
IMPLEMENT_SIMPLE_AUTOMATION_TEST(FTabForgeHttpTest, "TabForge.Protocol.NativeHTTP", EAutomationTestFlags::EditorContext | EAutomationTestFlags::EngineFilter)
bool FTabForgeHttpTest::RunTest(const FString&) {
    const FString URL = FPlatformMisc::GetEnvironmentVariable(TEXT("TABFORGE_TEST_URL"));
    const FString Path = FPlatformMisc::GetEnvironmentVariable(TEXT("TABFORGE_RUNTIME_JSON"));
    FString Runtime;
    if (URL.IsEmpty() || Path.IsEmpty() || !FFileHelper::LoadFileToString(Runtime, *Path)) { AddError(TEXT("Start platform acceptance server; set TABFORGE_TEST_URL and TABFORGE_RUNTIME_JSON")); return false; }
    ADD_LATENT_AUTOMATION_COMMAND(FHttpSmokeCommand(this, URL, Runtime));
    return true;
}
#endif

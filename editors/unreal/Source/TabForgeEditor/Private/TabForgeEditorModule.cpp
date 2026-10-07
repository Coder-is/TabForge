#include "Modules/ModuleManager.h"
#include "ToolMenus.h"
#include "Interfaces/IPluginManager.h"
#include "DesktopPlatformModule.h"
#include "IDesktopPlatform.h"
#include "HAL/PlatformProcess.h"
#include "HAL/FileManager.h"
#include "Containers/Ticker.h"
#include "Misc/FileHelper.h"
#include "Misc/MessageDialog.h"
#include "Misc/Paths.h"
#include "Settings/ProjectPackagingSettings.h"
#include "UObject/UObjectGlobals.h"

class FTabForgeEditorModule : public IModuleInterface {
    FProcHandle Process;
    void* ReadPipe = nullptr;
    void* WritePipe = nullptr;
    uint32 Pid = 0;
    FString Log, Source;
    bool bCancelled = false, bImports = false;
    FTSTicker::FDelegateHandle Ticker;

    static FString Argument(const FString& Flag, const FString& Value) {
        return TEXT("\"") + Flag + TEXT("=") + Value.Replace(TEXT("\""),TEXT("\\\"")) + TEXT("\"");
    }
    FString Tool() const {
        FString Platform, Name;
#if PLATFORM_WINDOWS
        Platform = PLATFORM_CPU_ARM_FAMILY ? TEXT("win32-arm64") : TEXT("win32-x64");
        Name = TEXT("tabforge.exe");
#elif PLATFORM_MAC
        Platform = PLATFORM_CPU_ARM_FAMILY ? TEXT("darwin-arm64") : TEXT("darwin-x64");
        Name = TEXT("tabforge");
#else
        return TEXT("");
#endif
        FString Local = FPaths::Combine(FPaths::ProjectDir(),TEXT("TabForge/Tools/TabForge"),Name);
        if (FPaths::FileExists(Local)) return FPaths::ConvertRelativePathToFull(Local);
        auto Plugin = IPluginManager::Get().FindPlugin(TEXT("TabForge"));
        return Plugin.IsValid() ? FPaths::ConvertRelativePathToFull(FPaths::Combine(Plugin->GetBaseDir(),TEXT("bin"),Platform,Name)) : TEXT("");
    }
    void CleanupLocks() {
        for (const FString& Lock : {FPaths::Combine(Source,TEXT(".tabforge-export.lock")), FPaths::Combine(FPaths::ProjectDir(),TEXT(".tabforge-client.lock"))}) {
            FString Owner;
            if (FFileHelper::LoadFileToString(Owner,*Lock) && Owner.TrimStartAndEnd() == FString::FromInt(static_cast<int32>(Pid))) IFileManager::Get().Delete(*Lock);
        }
    }
    void Finish(bool Show) {
        if (!Process.IsValid()) return;
        Log += FPlatformProcess::ReadPipe(ReadPipe);
        int32 Code = -1; FPlatformProcess::GetProcReturnCode(Process,&Code);
        FPlatformProcess::CloseProc(Process);
        Process = FProcHandle();
        FPlatformProcess::ClosePipe(ReadPipe,WritePipe); ReadPipe = WritePipe = nullptr;
        if (bCancelled) CleanupLocks();
        if (Code == 0 && bImports && !bCancelled) {
            auto Settings = GetMutableDefault<UProjectPackagingSettings>();
            const FString Directory = TEXT("TabForgeGenerated");
            if (!Settings->DirectoriesToAlwaysStageAsNonUFS.ContainsByPredicate([&](const FDirectoryPath& Path){ return Path.Path == Directory; })) {
                FDirectoryPath Path; Path.Path = Directory;
                Settings->DirectoriesToAlwaysStageAsNonUFS.Add(Path);
                Settings->UpdateDefaultConfigFile();
            }
        }
        if (Show) FMessageDialog::Open(EAppMsgType::Ok,FText::FromString((bCancelled ? TEXT("Cancelled\n") : Code == 0 ? TEXT("TabForge completed\n") : TEXT("TabForge failed\n")) + Log + TEXT("\nReport: ") + FPaths::Combine(FPaths::ProjectDir(),TEXT(".tabforge-report.json"))));
    }
    bool Tick(float) {
        Log += FPlatformProcess::ReadPipe(ReadPipe); if (Log.Len() > 100000) Log = Log.Right(100000);
        if (FPlatformProcess::IsProcRunning(Process)) return true;
        Finish(true); return false;
    }
    void Start(const FString& Action) {
        if (Process.IsValid()) { FMessageDialog::Open(EAppMsgType::Ok,FText::FromString(TEXT("A TabForge command is already running."))); return; }
        Source = FPaths::ConvertRelativePathToFull(FPaths::Combine(FPaths::ProjectDir(),TEXT("TabForge")));
        if (!FPaths::FileExists(FPaths::Combine(Source,TEXT("tabforge.json"))) && FPaths::FileExists(FPaths::Combine(FPaths::ProjectDir(),TEXT("tabforge.json")))) Source = FPaths::ConvertRelativePathToFull(FPaths::ProjectDir());
        FString Args;
        if (Action == TEXT("import")) {
            FString Selected; auto Desktop = FDesktopPlatformModule::Get();
            if (!Desktop || !Desktop->OpenDirectoryDialog(nullptr,TEXT("Select Generated bundle"),Source,Selected)) return;
            Args = Argument(TEXT("-import"),Selected);
        } else Args = Argument(Action == TEXT("init") ? TEXT("-init") : TEXT("-project"),Source);
        Args += TEXT(" -editor=unreal ") + Argument(TEXT("-editor_project"),FPaths::ConvertRelativePathToFull(FPaths::ProjectDir())) + TEXT(" -report");
        if (Action == TEXT("check")) Args += TEXT(" -check");
        const FString Executable = Tool();
        if (!FPaths::FileExists(Executable) || !FPlatformProcess::CreatePipe(ReadPipe,WritePipe)) { FMessageDialog::Open(EAppMsgType::Ok,FText::FromString(TEXT("No portable tool for this platform; install the matching TabForge plugin ZIP."))); return; }
        Log.Empty(); bCancelled = false; bImports = Action != TEXT("check");
        Process = FPlatformProcess::CreateProc(*Executable,*Args,true,true,true,&Pid,0,*FPaths::ProjectDir(),WritePipe,nullptr,WritePipe);
        if (!Process.IsValid()) { FPlatformProcess::ClosePipe(ReadPipe,WritePipe); ReadPipe = WritePipe = nullptr; FMessageDialog::Open(EAppMsgType::Ok,FText::FromString(TEXT("Cannot launch TabForge tool."))); return; }
        Ticker = FTSTicker::GetCoreTicker().AddTicker(FTickerDelegate::CreateRaw(this,&FTabForgeEditorModule::Tick));
    }
    void RegisterMenus() {
        FToolMenuOwnerScoped Owner(this);
        auto Menu = UToolMenus::Get()->ExtendMenu(TEXT("LevelEditor.MainMenu.Tools"));
        auto& Section = Menu->FindOrAddSection(TEXT("TabForge"));
        for (const auto& Pair : TMap<FString,FString>{{TEXT("init"),TEXT("Create complete example")},{TEXT("check"),TEXT("Validate project")},{TEXT("export"),TEXT("Export and import")},{TEXT("import"),TEXT("Import Generated bundle")}}) {
            const FString Action = Pair.Key;
            Section.AddMenuEntry(FName(*(TEXT("TabForge.")+Action)),FText::FromString(TEXT("TabForge: ")+Pair.Value),FText::GetEmpty(),FSlateIcon(),FUIAction(FExecuteAction::CreateLambda([this,Action]{Start(Action);})));
        }
        Section.AddMenuEntry(TEXT("TabForge.Cancel"),FText::FromString(TEXT("TabForge: Cancel")),FText::GetEmpty(),FSlateIcon(),FUIAction(FExecuteAction::CreateLambda([this]{ if (Process.IsValid()) { bCancelled = true; FPlatformProcess::TerminateProc(Process,true); } })));
        Section.AddMenuEntry(TEXT("TabForge.Report"),FText::FromString(TEXT("TabForge: Show report")),FText::GetEmpty(),FSlateIcon(),FUIAction(FExecuteAction::CreateLambda([]{ FString Report; FFileHelper::LoadFileToString(Report,*FPaths::Combine(FPaths::ProjectDir(),TEXT(".tabforge-report.json"))); FMessageDialog::Open(EAppMsgType::Ok,FText::FromString(Report)); })));
    }
public:
    void StartupModule() override { UToolMenus::RegisterStartupCallback(FSimpleMulticastDelegate::FDelegate::CreateRaw(this,&FTabForgeEditorModule::RegisterMenus)); }
    void ShutdownModule() override {
        UToolMenus::UnRegisterStartupCallback(this); UToolMenus::UnregisterOwner(this);
        FTSTicker::GetCoreTicker().RemoveTicker(Ticker);
        if (Process.IsValid()) { bCancelled = true; FPlatformProcess::TerminateProc(Process,true); FPlatformProcess::WaitForProc(Process); Finish(false); }
    }
};
IMPLEMENT_MODULE(FTabForgeEditorModule, TabForgeEditor)

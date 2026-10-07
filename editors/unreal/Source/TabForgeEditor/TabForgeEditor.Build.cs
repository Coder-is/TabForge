using UnrealBuildTool;
public class TabForgeEditor : ModuleRules {
    public TabForgeEditor(ReadOnlyTargetRules Target) : base(Target) {
        PCHUsage = PCHUsageMode.UseExplicitOrSharedPCHs;
        PrivateDependencyModuleNames.AddRange(new [] { "Core", "CoreUObject", "Engine", "UnrealEd", "ToolMenus", "Projects", "DesktopPlatform", "Slate", "SlateCore", "DeveloperToolSettings" });
    }
}

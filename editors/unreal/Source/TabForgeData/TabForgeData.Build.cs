using UnrealBuildTool;
public class TabForgeData : ModuleRules {
    public TabForgeData(ReadOnlyTargetRules Target) : base(Target) {
        PCHUsage = PCHUsageMode.UseExplicitOrSharedPCHs;
        PublicDependencyModuleNames.AddRange(new [] { "Core", "Json" });
        PrivateDependencyModuleNames.AddRange(new [] { "JsonUtilities" });
    }
}

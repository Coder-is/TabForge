using UnrealBuildTool;

public class TabForgeProtocol : ModuleRules
{
    public TabForgeProtocol(ReadOnlyTargetRules Target) : base(Target)
    {
        PCHUsage = PCHUsageMode.UseExplicitOrSharedPCHs;
        PublicDependencyModuleNames.AddRange(new[] { "Core", "HTTP", "Json" });
        PrivateDependencyModuleNames.AddRange(new[] { "CoreUObject", "Engine" });
    }
}

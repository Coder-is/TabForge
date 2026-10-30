#if !UNITY_5_3_OR_NEWER
using System;
using System.IO;
using System.Linq;
using System.Text;
using Newtonsoft.Json.Linq;
using TabForge.Protocol;

// Data loader rules shared with Go, Node.js, Java and Python (Go protojson decides).
internal static class DataTests
{
    private static void Check(bool condition, string message) { if (!condition) throw new Exception(message); }

    public static void Run(string root)
    {
        var loader = new DataSchema(File.ReadAllText(Path.Combine(root, "examples", "complete", "Generated", "schema", "wire_schema.json")));
        var cases = JArray.Parse(File.ReadAllText(Path.Combine(root, "examples", "backend", "cases.json")));
        foreach (var item in cases)
        {
            var accepted = true;
            try { loader.Decode((string)item["message"], (string)item["json"]); } catch (ProtocolException) { accepted = false; }
            Check(accepted == (bool)item["valid"], "Shared wire case: " + (string)item["json"]);
        }

        var normalized = loader.Decode("tabforge.demo.config.Item", @"{""id"":""1e2"",""ownerId"":18446744073709551615,""signedTotal"":-9.223372036854775808e18,""rarity"":1.0,""levels"":[1.0,""2""],""iconHash"":""AAF"",""priority"":""7"",""price"":""1.5"",""name"":null,""extra"":{""a"":1e0}}");
        var expected = JObject.Parse(@"{""id"":100,""ownerId"":""18446744073709551615"",""signedTotal"":""-9223372036854775808"",""rarity"":1,""levels"":[1,2],""iconHash"":""AAE="",""priority"":7,""price"":1.5,""extra"":{""a"":1.0}}");
        Check(JToken.DeepEquals(normalized, expected), "Canonical ProtoJSON: " + normalized.ToString(Newtonsoft.Json.Formatting.None));
        Check(normalized.SelectTokens("$..*").All(token => token.Type != JTokenType.Raw), "Raw numbers must be normalized");
        var mapped = loader.Decode("tabforge.demo.config.Tables", @"{""byId"":{""+1"":{},""02"":{}}}");
        Check(string.Join(",", ((JObject)mapped["byId"]).Properties().Select(p => p.Name)) == "1,2", "Canonical map keys");

        // 20,000 rows, 180,002 JSON values: above the network budget, inside the data budget.
        var rows = new StringBuilder(@"{""items"":[");
        for (var i = 1; i <= 20000; i++)
            rows.Append(i > 1 ? "," : "").Append(@"{""id"":").Append(i).Append(@",""name"":""item ").Append(i).Append(@""",""ownerId"":""").Append(i).Append(@""",""levels"":[1,2,3],""price"":1.5}");
        var large = rows.Append("]}").ToString();
        Check(((JArray)loader.Decode("tabforge.demo.config.Tables", large)["items"]).Count == 20000, "Large data file");
        var rejected = false;
        try { Json.Parse(large); } catch (ProtocolException error) { rejected = error.Code == "invalid_json"; }
        Check(rejected, "Network budget must reject the large file");

        Func<int, string> nested = depth => new string('[', depth) + "0" + new string(']', depth);
        Check(new JsonSyntax(nested(64), WireValidator.MaxDepth, WireValidator.DataMaxNodes).Valid(), "Depth 64");
        Check(!new JsonSyntax(nested(65), WireValidator.MaxDepth, WireValidator.DataMaxNodes).Valid(), "Depth 65");
        Check(!new JsonSyntax("[0,0,0,0,0,0,0,0,0,0]", 64, 10).Valid(), "Node budget");
        Check(!new JsonSyntax(@"{""x"":1,""x"":2}").Valid(), "Duplicate after unescaping");
        Check(WireValidator.ProtoInteger("0.000000000000000000001e21", true, 64) == null, "Go 20-digit cap");
        Check(WireValidator.ProtoInteger("1.8446744073709551615e19", false, 64) == "18446744073709551615", "Exact uint64");
        Check(WireValidator.ProtoInteger("-0.0", false, 32) == "0" && WireValidator.ProtoInteger("1e-2", true, 32) == null, "Zero and fractions");
        Check(WireValidator.ProtoMapKey("+1", "uint32") == null && WireValidator.ProtoMapKey("-0", "int32") == "0", "Go map keys");
        Check(WireValidator.ProtoBytes("-_-_") == "+/+/" && WireValidator.ProtoBytes("AAF=") == "AAE=" && WireValidator.ProtoBytes("AA=") == null, "Go bytes rule");
        Console.WriteLine("C# data loader units passed (" + cases.Count + " shared cases)");
    }
}
#endif

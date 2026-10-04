import io.tabforge.data.DataBundle;
import java.nio.file.Path;

public final class Client {
  public static void main(String[] args) throws Exception {
    var bundle =
        DataBundle.open(Path.of(args.length > 0 ? args[0] : "examples/complete/Generated"));
    var tables = bundle.read("data/tables.json", "tabforge.demo.config.Tables").getAsJsonObject();
    var item = tables.getAsJsonArray("items").get(0).getAsJsonObject();
    System.out.println(
        item.get("name").getAsString() + ": ownerId=" + item.get("ownerId").getAsString());
  }
}

package io.tabforge.data;

import com.google.gson.*;
import java.nio.file.*;
import java.util.concurrent.*;

/** Standalone external-consumer test: also run against the packaged JAR. */
public final class BackendSmoke {
  private static void require(boolean value, String message) {
    if (!value) throw new AssertionError(message);
  }

  private static void rejects(Checked work) throws Exception {
    try {
      work.run();
    } catch (java.io.IOException | IllegalArgumentException error) {
      return;
    }
    throw new AssertionError("Invalid input accepted");
  }

  @FunctionalInterface
  private interface Checked {
    void run() throws Exception;
  }

  public static void main(String[] args) throws Exception {
    Path root = Path.of(args[0]);
    DataStore store = new DataStore();
    DataBundle bundle = store.reload(root, "");
    JsonObject tables =
        bundle.read("data/tables.json", "tabforge.demo.config.Tables").getAsJsonObject();
    JsonObject item = tables.getAsJsonArray("items").get(0).getAsJsonObject();
    require(item.get("ownerId").getAsString().equals("18446744073709551615"), "uint64 precision");
    require(
        item.get("signedTotal").getAsString().equals("-9223372036854775808"), "int64 precision");
    require(
        item.get("unlockLevel").getAsInt() == 0
            && !tables.getAsJsonArray("items").get(1).getAsJsonObject().has("unlockLevel"),
        "optional presence");
    item.addProperty("name", "mutated");
    require(
        bundle
            .read("data/tables.json")
            .getAsJsonObject()
            .getAsJsonArray("items")
            .get(0)
            .getAsJsonObject()
            .get("name")
            .getAsString()
            .equals("新手剑😀"),
        "immutable snapshot");
    rejects(() -> bundle.read("data/tables.pbb"));
    rejects(() -> bundle.read("missing"));
    rejects(() -> bundle.read("data/tables.json", "Wrong"));
    rejects(() -> DataBundle.open(root, "0".repeat(64)));
    JsonArray cases = JsonParser.parseString(Files.readString(Path.of(args[1]))).getAsJsonArray();
    for (JsonElement element : cases) {
      JsonObject c = element.getAsJsonObject();
      if (c.get("valid").getAsBoolean())
        bundle.decode(c.get("message").getAsString(), c.get("json").getAsString());
      else
        rejects(() -> bundle.decode(c.get("message").getAsString(), c.get("json").getAsString()));
    }
    ExecutorService pool = Executors.newFixedThreadPool(4);
    try {
      for (int i = 0; i < 16; i++)
        require(
            pool.submit(
                        () ->
                            bundle
                                .read("data/tables.json")
                                .getAsJsonObject()
                                .getAsJsonArray("items")
                                .size())
                    .get()
                == 3,
            "concurrent read");
    } finally {
      pool.shutdown();
    }
    Path data = root.resolve("data/tables.json");
    byte[] original = Files.readAllBytes(data);
    byte[] invalid = "{\"unknown\":1}".getBytes(java.nio.charset.StandardCharsets.UTF_8);
    try {
      Files.write(data, invalid);
      rejects(() -> store.reload(root, ""));
      require(store.snapshot() == bundle, "failed reload changed snapshot");
      Path manifestPath = root.resolve("data_manifest.json");
      String raw = Files.readString(manifestPath);
      JsonObject manifest = JsonParser.parseString(raw).getAsJsonObject();
      try {
        for (JsonElement element : manifest.getAsJsonArray("data")) {
          JsonObject e = element.getAsJsonObject();
          if (e.get("path").getAsString().equals("data/tables.json"))
            e.addProperty(
                "sha256",
                java.util.HexFormat.of()
                    .formatHex(java.security.MessageDigest.getInstance("SHA-256").digest(invalid)));
        }
        Files.writeString(manifestPath, manifest.toString());
        rejects(() -> DataBundle.open(root));
        manifest.getAsJsonArray("data").get(0).getAsJsonObject().addProperty("path", "../escape");
        Files.writeString(manifestPath, manifest.toString());
        rejects(() -> DataBundle.open(root));
      } finally {
        Files.writeString(manifestPath, raw);
      }
    } finally {
      Files.write(data, original);
    }
    System.out.println("TabForge Java backend: " + cases.size() + " wire cases passed");
  }
}

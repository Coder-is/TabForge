package io.tabforge.data;

import com.google.gson.*;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.nio.file.*;
import java.security.*;
import java.util.*;

/** Immutable, validated ProtoJSON snapshot of a Generated directory. */
public final class DataBundle {
  public record Entry(String path, String sha256, String message, String encoding) {}

  private final String schemaHash;
  private final List<Entry> entries;
  private final Map<String, JsonElement> values;
  private final SchemaValidator validator;

  private DataBundle(
      String hash,
      List<Entry> entries,
      Map<String, JsonElement> values,
      SchemaValidator validator) {
    this.schemaHash = hash;
    this.entries = List.copyOf(entries);
    this.values = Map.copyOf(values);
    this.validator = validator;
  }

  public static DataBundle open(Path directory) throws IOException {
    return open(directory, "");
  }

  public static DataBundle open(Path directory, String expectedHash) throws IOException {
    Reader reader = new Reader(directory);
    JsonObject manifest = StrictJson.object(StrictJson.parse(reader.read("data_manifest.json")));
    String hash = StrictJson.string(manifest.get("schemaHash"));
    if (!"tabforge.data.v1".equals(StrictJson.string(manifest.get("format")))
        || !hash(hash)
        || (!expectedHash.isEmpty() && !expectedHash.equals(hash)))
      throw new IOException("Invalid manifest or schema hash mismatch");
    reader.checked(StrictJson.object(manifest.get("descriptor")));
    JsonObject wire =
        StrictJson.object(
            StrictJson.parse(reader.checked(StrictJson.object(manifest.get("wireSchema")))));
    StrictJson.object(wire.get("messages"));
    StrictJson.object(wire.get("enums"));
    SchemaValidator validator = new SchemaValidator(wire);
    List<Entry> entries = new ArrayList<>();
    Map<String, JsonElement> values = new HashMap<>();
    if (!manifest.has("data") || !manifest.get("data").isJsonArray())
      throw new IOException("Invalid data entries");
    for (JsonElement item : manifest.getAsJsonArray("data")) {
      JsonObject file = StrictJson.object(item);
      byte[] bytes = reader.checked(file);
      Entry entry =
          new Entry(
              StrictJson.string(file.get("path")),
              StrictJson.string(file.get("sha256")),
              StrictJson.string(file.get("message")),
              StrictJson.string(file.get("encoding")));
      if (!wire.getAsJsonObject("messages").has(entry.message()))
        throw new IOException("Unknown message: " + entry.message());
      if (entry.encoding().equals("protojson")) {
        JsonElement value = StrictJson.parse(bytes);
        validator.validate(entry.message(), value);
        values.put(entry.path(), value);
      } else if (!entry.encoding().equals("protobuf"))
        throw new IOException("Unsupported encoding: " + entry.encoding());
      entries.add(entry);
    }
    return new DataBundle(hash, entries, values, validator);
  }

  public String schemaHash() {
    return schemaHash;
  }

  public List<Entry> entries() {
    return entries;
  }

  public JsonElement read(String name) {
    return read(name, "");
  }

  public JsonElement read(String name, String expectedMessage) {
    Entry entry =
        entries.stream()
            .filter(item -> item.path().equals(name))
            .findFirst()
            .orElseThrow(() -> new IllegalArgumentException("Data file not declared: " + name));
    if (!expectedMessage.isEmpty() && !entry.message().equals(expectedMessage))
      throw new IllegalArgumentException("Message type mismatch");
    if (!entry.encoding().equals("protojson"))
      throw new IllegalArgumentException(
          "Java data loader supports ProtoJSON; use the matching .json entry");
    return values.get(name).deepCopy();
  }

  public JsonElement decode(String message, String text) throws IOException {
    JsonElement value = StrictJson.parse(text.getBytes(StandardCharsets.UTF_8));
    validator.validate(message, value);
    return value;
  }

  private static boolean hash(String value) {
    return value.matches("[a-f0-9]{64}");
  }

  private static boolean local(String name) {
    if (name.isEmpty() || name.indexOf('\\') >= 0 || name.indexOf(':') >= 0) return false;
    for (String part : name.split("/", -1))
      if (part.isEmpty() || part.equals(".") || part.equals("..")) return false;
    return true;
  }

  private static final class Reader {
    final Path root;
    final Set<String> seen = new HashSet<>(Set.of("data_manifest.json"));

    Reader(Path root) {
      this.root = root;
    }

    byte[] read(String name) throws IOException {
      if (!local(name)) throw new IOException("Invalid bundle path: " + name);
      Path current = root;
      for (String part : name.split("/")) {
        current = current.resolve(part);
        if (Files.isSymbolicLink(current)) throw new IOException("Symlink bundle path: " + name);
      }
      if (!Files.isRegularFile(current, LinkOption.NOFOLLOW_LINKS))
        throw new IOException("Not a regular bundle file: " + name);
      return Files.readAllBytes(current);
    }

    byte[] checked(JsonObject file) throws IOException {
      String name = StrictJson.string(file.get("path")),
          expected = StrictJson.string(file.get("sha256"));
      if (!local(name) || !hash(expected) || !seen.add(name.toLowerCase(Locale.ROOT)))
        throw new IOException("Invalid or duplicate manifest file: " + name);
      byte[] data = read(name);
      try {
        if (!HexFormat.of()
            .formatHex(MessageDigest.getInstance("SHA-256").digest(data))
            .equals(expected)) throw new IOException("Checksum mismatch: " + name);
      } catch (NoSuchAlgorithmException error) {
        throw new AssertionError(error);
      }
      return data;
    }
  }
}

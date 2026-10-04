package io.tabforge.data;

import java.io.IOException;
import java.nio.file.Path;
import java.util.concurrent.atomic.AtomicReference;

/** Keep one snapshot per request. Failed reloads preserve the previous snapshot. */
public final class DataStore {
  private final AtomicReference<DataBundle> current = new AtomicReference<>();

  public DataBundle snapshot() {
    return current.get();
  }

  public DataBundle reload(Path directory, String expectedHash) throws IOException {
    DataBundle next = DataBundle.open(directory, expectedHash);
    current.set(next);
    return next;
  }
}

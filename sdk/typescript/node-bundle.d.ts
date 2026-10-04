export interface DataEntry {
    path: string;
    sha256: string;
    message: string;
    encoding: "protojson" | "protobuf";
}
export declare class DataBundle<T extends object = Record<string, unknown>> {
    private constructor();
    static open<T extends object = Record<string, unknown>>(directory: string, expectedHash?: string): Promise<DataBundle<T>>;
    readonly schemaHash: string;
    entries(): DataEntry[];
    read(name: string): unknown;
    read<K extends keyof T & string>(name: string, expectedMessage: K): T[K];
    decode<K extends keyof T & string>(message: K, json: string): T[K];
}
export declare class DataStore<T extends object = Record<string, unknown>> {
    readonly snapshot: DataBundle<T> | undefined;
    reload(directory: string, expectedHash?: string): Promise<DataBundle<T>>;
}

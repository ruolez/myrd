// Minimal IndexedDB key/value helper. DB "remotectl", stores per PROTOCOL §4.

const DB_NAME = "remotectl";
const STORES = ["client_keys", "device_pins"] as const;
export type StoreName = (typeof STORES)[number];

let dbPromise: Promise<IDBDatabase> | null = null;

function open(): Promise<IDBDatabase> {
  dbPromise ??= new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => {
      for (const s of STORES)
        if (!req.result.objectStoreNames.contains(s)) req.result.createObjectStore(s);
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => {
      dbPromise = null;
      reject(req.error ?? new Error("IndexedDB unavailable"));
    };
  });
  return dbPromise;
}

function tx<T>(
  store: StoreName,
  mode: IDBTransactionMode,
  fn: (s: IDBObjectStore) => IDBRequest,
): Promise<T> {
  return open().then(
    (db) =>
      new Promise<T>((resolve, reject) => {
        const req = fn(db.transaction(store, mode).objectStore(store));
        req.onsuccess = () => resolve(req.result as T);
        req.onerror = () => reject(req.error ?? new Error("IndexedDB request failed"));
      }),
  );
}

export const idbGet = <T>(store: StoreName, key: string) =>
  tx<T | undefined>(store, "readonly", (s) => s.get(key));
export const idbPut = (store: StoreName, key: string, value: unknown) =>
  tx<IDBValidKey>(store, "readwrite", (s) => s.put(value, key));
export const idbDelete = (store: StoreName, key: string) =>
  tx<undefined>(store, "readwrite", (s) => s.delete(key));

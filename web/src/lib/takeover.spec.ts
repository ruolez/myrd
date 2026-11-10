import { describe, expect, test } from "vitest";
import type { ClientKeyId } from "../api/types";
import { describeHolder } from "./takeover";
import type { Holder } from "./takeover";

const NOW = Date.parse("2026-11-10T12:00:00Z");
const MINE = "key-mine" as ClientKeyId;
const OTHER = "key-other" as ClientKeyId;

const active: Holder = {
  client_key_id: OTHER,
  client_key_label: "Chrome on Windows",
  client_ip: "203.0.113.7",
  mode: "desktop",
  state: "active",
  created_at: "2026-11-10T09:59:00Z",
  connected_at: "2026-11-10T10:00:00Z",
};

describe("describeHolder", () => {
  test("names the other browser, what it opened, since when and from where", () => {
    expect(describeHolder(active, MINE, NOW)).toEqual({
      who: "Chrome on Windows",
      detail: "Screen session · connected 2h ago · from 203.0.113.7",
    });
  });

  test("recognises this browser's own key", () => {
    expect(describeHolder({ ...active, client_key_id: MINE }, MINE, NOW).who).toBe("This browser");
  });

  test("a pending terminal without label or address", () => {
    expect(
      describeHolder(
        {
          ...active,
          client_key_id: null,
          client_key_label: null,
          client_ip: null,
          mode: "terminal",
          state: "pending",
          created_at: "2026-11-10T11:59:00Z",
          connected_at: null,
        },
        null,
        NOW,
      ),
    ).toEqual({ who: "Another browser", detail: "Terminal session · connecting since 1m ago" });
  });
});

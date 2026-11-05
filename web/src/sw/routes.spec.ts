import fc from "fast-check";
import { describe, expect, test } from "vitest";
import { classifyRequest, staleCaches } from "./routes";

const ORIGIN = "https://myrd.example";
const at = (path: string, origin = ORIGIN) => new URL(path, origin);
// No leading dot: "." and ".." are dot-segments that URL parsing removes.
const segment = fc.stringMatching(/^[A-Za-z0-9_~-][A-Za-z0-9._~-]{0,11}$/);
const subpath = fc.array(segment, { maxLength: 4 }).map((s) => s.join("/"));
const modes: RequestMode[] = ["navigate", "same-origin", "no-cors", "cors"];

describe("classifyRequest", () => {
  test.each([
    ["/devices", "navigate", "shell"],
    ["/session/abc?x=1", "navigate", "shell"],
    ["/i/ABCD-EFGH-JKLM", "navigate", "shell"],
    ["/assets/index-abc123.js", "same-origin", "asset"],
    ["/assets/TerminalDeck-abc.css", "no-cors", "asset"],
    ["/favicon.svg", "no-cors", "static"],
    ["/manifest.webmanifest", "cors", "static"],
    ["/icons/icon-192.png", "no-cors", "static"],
    ["/sw.js", "same-origin", "bypass"],
    ["/dl-sw.js", "same-origin", "bypass"],
    ["/healthz", "same-origin", "bypass"],
    ["/v1/devices", "same-origin", "bypass"],
    ["/__dl/abc/file.bin", "navigate", "bypass"],
    ["/.well-known/acme-challenge/token", "navigate", "bypass"],
    ["/something-else.txt", "same-origin", "bypass"],
  ] as const)("%s (%s) is %s", (path, mode, want) => {
    expect(classifyRequest(at(path), "GET", mode, ORIGIN)).toBe(want);
  });

  test("API, download and ACME paths are bypassed in every mode", () => {
    fc.assert(
      fc.property(
        fc.constantFrom("/v1/", "/__dl/", "/.well-known/"),
        subpath,
        fc.constantFrom(...modes),
        (prefix, rest, mode) =>
          classifyRequest(at(prefix + rest), "GET", mode, ORIGIN) === "bypass",
      ),
    );
  });

  test("non-GET requests are bypassed on every path", () => {
    fc.assert(
      fc.property(
        fc.constantFrom("POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"),
        subpath,
        fc.constantFrom(...modes),
        (method, rest, mode) => classifyRequest(at(`/${rest}`), method, mode, ORIGIN) === "bypass",
      ),
    );
  });

  test("cross-origin requests are bypassed on every path", () => {
    fc.assert(
      fc.property(subpath, fc.constantFrom(...modes), (rest, mode) => {
        const url = at(`/assets/${rest}`, "https://cdn.example");
        return classifyRequest(url, "GET", mode, ORIGIN) === "bypass";
      }),
    );
  });
});

describe("staleCaches", () => {
  test.each([
    { names: [], current: "shell-c", want: [] },
    { names: ["shell-c"], current: "shell-c", want: [] },
    { names: ["shell-b", "shell-c"], current: "shell-c", want: [] },
    { names: ["shell-a", "shell-b", "shell-c"], current: "shell-c", want: ["shell-a"] },
    {
      names: ["shell-a", "shell-b", "shell-c", "shell-d"],
      current: "shell-d",
      want: ["shell-a", "shell-b"],
    },
    { names: ["other", "shell-a", "shell-b", "shell-c"], current: "shell-c", want: ["shell-a"] },
    { names: ["shell-a", "shell-c", "shell-b"], current: "shell-a", want: ["shell-c"] },
  ])("keeps $current and the newest older build of $names", ({ names, current, want }) => {
    expect(staleCaches(names, current)).toEqual(want);
  });

  test("never deletes the current build, foreign caches, or more than all but one older build", () => {
    fc.assert(
      fc.property(
        fc.uniqueArray(
          fc.oneof(
            segment.map((s) => `shell-${s}`),
            segment,
          ),
          { maxLength: 8 },
        ),
        (names) => {
          const current = names.find((n) => n.startsWith("shell-")) ?? "shell-current";
          const stale = staleCaches(names, current);
          const older = names.filter((n) => n.startsWith("shell-") && n !== current);
          return (
            !stale.includes(current) &&
            stale.every((n) => n.startsWith("shell-")) &&
            stale.length === Math.max(0, older.length - 1)
          );
        },
      ),
    );
  });
});

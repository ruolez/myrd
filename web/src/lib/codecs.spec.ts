import { describe, expect, test } from "vitest";
import { codecName, preferredCodecs } from "./codecs";

// Chrome 143's receive codecs, in its order.
const chrome = [
  { mimeType: "video/VP8" },
  { mimeType: "video/rtx" },
  { mimeType: "video/VP9", sdpFmtpLine: "profile-id=0" },
  { mimeType: "video/VP9", sdpFmtpLine: "profile-id=2" },
  { mimeType: "video/VP9", sdpFmtpLine: "profile-id=1" },
  { mimeType: "video/VP9", sdpFmtpLine: "profile-id=3" },
  {
    mimeType: "video/H264",
    sdpFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
  },
  {
    mimeType: "video/H264",
    sdpFmtpLine: "level-asymmetry-allowed=1;packetization-mode=0;profile-level-id=42001f",
  },
  {
    mimeType: "video/H264",
    sdpFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
  },
  {
    mimeType: "video/H264",
    sdpFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=4d001f",
  },
  {
    mimeType: "video/H264",
    sdpFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=64001f",
  },
  {
    mimeType: "video/H264",
    sdpFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=f4001f",
  },
  { mimeType: "video/AV1", sdpFmtpLine: "level-idx=5;profile=1;tier=0" },
  { mimeType: "video/red" },
  { mimeType: "video/ulpfec" },
  { mimeType: "video/flexfec-03", sdpFmtpLine: "repair-window=10000000" },
];

const describeCodec = (c: { mimeType: string; sdpFmtpLine?: string }) =>
  `${c.mimeType.slice(6)} ${(c.sdpFmtpLine ?? "").match(/profile-(level-)?id=\w+|packetization-mode=0/)?.[0] ?? ""}`.trim();

describe("preferredCodecs", () => {
  test("H.264 high first, then full-color VP9, then repair formats", () => {
    expect(preferredCodecs(chrome)?.map(describeCodec)).toEqual([
      "H264 profile-level-id=64001f",
      "H264 profile-level-id=4d001f",
      "H264 profile-level-id=42e01f",
      "H264 profile-level-id=42001f",
      "H264 profile-level-id=f4001f",
      "H264 packetization-mode=0",
      "VP9 profile-id=1",
      "rtx",
      "red",
      "ulpfec",
      "flexfec-03",
    ]);
  });

  test("leaves the browser's defaults without H.264", () => {
    expect(preferredCodecs(chrome.filter((c) => c.mimeType !== "video/H264"))).toBeNull();
  });

  test("no full color without VP9 profile 1", () => {
    const noProfile1 = chrome.filter((c) => c.sdpFmtpLine !== "profile-id=1");
    expect(preferredCodecs(noProfile1)?.some((c) => c.mimeType === "video/VP9")).toEqual(false);
  });
});

describe("codecName", () => {
  test.each([
    [
      "video/H264",
      "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=64001f",
      "H.264",
    ],
    ["video/VP9", "profile-id=1", "VP9 4:4:4"],
    ["video/VP9", "profile-id=0", "VP9"],
    ["video/AV1", undefined, "AV1"],
    [undefined, undefined, "—"],
  ])("%s %s", (mime, fmtp, want) => {
    expect(codecName(mime, fmtp)).toEqual(want);
  });
});

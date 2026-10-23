// Saving a sign-in outside a session: the unlock key comes from the server,
// so it counts only when the device key this browser pinned signed it.

import type { SignedUnlockKey, UserId } from "../api/types";
import { ensureClientKey } from "./clientKey";
import { checkDevicePin } from "./devicePins";
import { sealCredential, verifyUnlockKey } from "./unlockCrypto";
import type { SealedCredential } from "./unlockCrypto";

export async function sealForDevice(o: {
  deviceId: string;
  deviceName: string;
  devicePublicKey: string;
  unlockKey: SignedUnlockKey;
  userId: UserId;
  username: string;
  password: string;
}): Promise<SealedCredential> {
  const pin = await checkDevicePin(o.deviceId, o.devicePublicKey);
  if (pin.status === "new")
    throw new Error(`Connect to ${o.deviceName} once from this browser, then save its sign-in.`);
  if (pin.status === "mismatch")
    throw new Error(`${o.deviceName} has a different identity key than this browser pinned.`);
  const unlockKey = await verifyUnlockKey(
    o.devicePublicKey,
    o.unlockKey.public_key,
    o.unlockKey.signature,
  );
  if (!unlockKey) throw new Error(`${o.deviceName}'s unlock key is not signed by its device key.`);
  const ck = await ensureClientKey(o.userId);
  return sealCredential({
    unlockKey,
    clientSpki: ck.spki,
    clientKey: ck.keyPair.privateKey,
    userId: o.userId,
    username: o.username,
    password: o.password,
  });
}

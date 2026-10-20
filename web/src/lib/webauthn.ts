// WebAuthn JSON ↔ browser credential conversion. Uses the native
// parse*OptionsFromJSON / toJSON when available, manual base64url otherwise.

import { b64urlToBytes, bytesToB64url } from "./fingerprint";

type Json = Record<string, unknown>;

type PKCStatic = typeof PublicKeyCredential & {
  parseRequestOptionsFromJSON?: (o: unknown) => PublicKeyCredentialRequestOptions;
  parseCreationOptionsFromJSON?: (o: unknown) => PublicKeyCredentialCreationOptions;
};

/** The server may wrap options as {publicKey:{...}} (go-webauthn) or send them bare. */
function unwrap(o: unknown): Json {
  const j = (o ?? {}) as Json;
  return (j.publicKey as Json | undefined) ?? j;
}

const desc = (list: unknown) =>
  Array.isArray(list)
    ? list.map(
        (c: Json) =>
          ({ ...c, id: b64urlToBytes(c.id as string) }) as unknown as PublicKeyCredentialDescriptor,
      )
    : undefined;

export function parseRequestOptions(raw: unknown): PublicKeyCredentialRequestOptions {
  const o = unwrap(raw);
  const P = window.PublicKeyCredential as PKCStatic | undefined;
  if (P?.parseRequestOptionsFromJSON) return P.parseRequestOptionsFromJSON(o);
  return {
    ...(o as object),
    challenge: b64urlToBytes(o.challenge as string),
    allowCredentials: desc(o.allowCredentials),
  } as PublicKeyCredentialRequestOptions;
}

export function parseCreationOptions(raw: unknown): PublicKeyCredentialCreationOptions {
  const o = unwrap(raw);
  const P = window.PublicKeyCredential as PKCStatic | undefined;
  if (P?.parseCreationOptionsFromJSON) return P.parseCreationOptionsFromJSON(o);
  const user = o.user as Json;
  return {
    ...(o as object),
    challenge: b64urlToBytes(o.challenge as string),
    user: { ...user, id: b64urlToBytes(user.id as string) },
    excludeCredentials: desc(o.excludeCredentials),
  } as unknown as PublicKeyCredentialCreationOptions;
}

export function credentialToJSON(cred: PublicKeyCredential): Json {
  const withJson = cred as unknown as { toJSON?: () => Json };
  if (typeof withJson.toJSON === "function") {
    try {
      return withJson.toJSON();
    } catch {
      // fall through to manual encoding
    }
  }
  const r = cred.response;
  const response: Json = { clientDataJSON: bytesToB64url(r.clientDataJSON) };
  if ("attestationObject" in r) {
    const a = r as AuthenticatorAttestationResponse;
    response.attestationObject = bytesToB64url(a.attestationObject);
    response.transports = a.getTransports?.() ?? [];
  } else {
    const a = r as AuthenticatorAssertionResponse;
    response.authenticatorData = bytesToB64url(a.authenticatorData);
    response.signature = bytesToB64url(a.signature);
    response.userHandle = a.userHandle ? bytesToB64url(a.userHandle) : null;
  }
  return {
    id: cred.id,
    rawId: bytesToB64url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? null,
    clientExtensionResults: cred.getClientExtensionResults(),
    response,
  };
}

export const passkeysSupported = () =>
  typeof window !== "undefined" && typeof window.PublicKeyCredential === "function";

export async function getAssertion(options: unknown): Promise<Json> {
  const cred = (await navigator.credentials.get({
    publicKey: parseRequestOptions(options),
  })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No passkey selected.");
  return credentialToJSON(cred);
}

export async function createCredential(options: unknown): Promise<Json> {
  const cred = (await navigator.credentials.create({
    publicKey: parseCreationOptions(options),
  })) as PublicKeyCredential | null;
  if (!cred) throw new Error("Passkey creation was cancelled.");
  return credentialToJSON(cred);
}

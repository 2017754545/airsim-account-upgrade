// Account layer for the self-hosted relay.
//
// Invitation-code semantics adopted from OpenHanako's alpha-gate-worker
// (https://github.com/liliMozi/openhanako, tools/alpha-gate-worker/core.mjs,
// Apache License 2.0): unbiased human-friendly code generation, uniform
// rejection messages so the endpoint cannot be mined as a code oracle,
// idempotent re-redemption for the same device, and a fixed-window rate
// limiter whose reset instant lives inside the stored record.
// Device accounts, approval and revocation are relay-specific extensions.
//
// Storage layout (via relay state store / DEVICES KV):
//   account:{account_id}      -> account record
//   invite:{code_hash}        -> invitation record (the code itself is never stored)
//   invite-index              -> { hashes: [code_hash, ...] }
//   revoked-device:{id}       -> tombstone with long TTL
//   ratelimit:{bucket}        -> { count, resetAt } fixed-window counter

import { sha256Hex } from "./auth-crypto.mjs";
import {
  readRelayState,
  writeRelayState,
  deleteRelayState,
  readDeviceRecord,
  writeDeviceRecord,
} from "./status-store.mjs";
import { recordDashboardEvent } from "./dashboard.mjs";

const CODE_ALPHABET = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"; // 32 glyphs, no 0/O/1/I
const CODE_PREFIX = "ASR";
const CODE_GROUPS = 3;
const CODE_GROUP_LENGTH = 4;
const CODE_PATTERN = new RegExp(
  `^${CODE_PREFIX}(?:-[${CODE_ALPHABET}]{${CODE_GROUP_LENGTH}}){${CODE_GROUPS}}$`,
);
const ACCOUNT_PREFIX = "account:";
const INVITE_PREFIX = "invite:";
const INVITE_INDEX_KEY = "invite-index";
const REVOKED_PREFIX = "revoked-device:";
const RATELIMIT_PREFIX = "ratelimit:";
const DEFAULT_INVITE_TTL_HOURS = 24;
const MAX_INVITES_PER_ACCOUNT = 200;
const ACCOUNT_ID_PATTERN = /^[a-z0-9][a-z0-9_-]{1,63}$/i;

// One wording for "not a code", "already spent", "revoked" and "expired".
// Splitting these apart would turn registration into a code oracle.
const INVALID_INVITE_MESSAGE = "invitation code is invalid or no longer usable";

export function isValidAccountID(accountID) {
  return typeof accountID === "string" && ACCOUNT_ID_PATTERN.test(accountID);
}

export function normalizeInvitationCode(value) {
  return String(value ?? "").trim().toUpperCase();
}

function defaultRandomBytes(length) {
  const bytes = new Uint8Array(length);
  crypto.getRandomValues(bytes);
  return bytes;
}

// 32 glyphs divide 256 evenly, so the modulo carries no bias.
function generateInvitationCode(randomBytes = defaultRandomBytes) {
  const length = CODE_GROUPS * CODE_GROUP_LENGTH;
  const bytes = randomBytes(length);
  const groups = [];
  for (let group = 0; group < CODE_GROUPS; group += 1) {
    let text = "";
    for (let index = 0; index < CODE_GROUP_LENGTH; index += 1) {
      text += CODE_ALPHABET[bytes[group * CODE_GROUP_LENGTH + index] % CODE_ALPHABET.length];
    }
    groups.push(text);
  }
  return [CODE_PREFIX, ...groups].join("-");
}

/**
 * Fixed-window rate limit keyed by an arbitrary bucket (device id, IP, …).
 * The reset instant lives inside the record, so a burst of requests cannot
 * slide the window forward.
 */
export async function enforceFixedWindowRateLimit(env, bucket, limit, windowSeconds) {
  const key = `${RATELIMIT_PREFIX}${bucket}`;
  const nowMS = Date.now();
  let count = 0;
  let resetAt = nowMS + windowSeconds * 1000;
  const raw = await readRelayState(env, key);
  if (raw) {
    try {
      const record = JSON.parse(raw);
      if (Number.isFinite(record.resetAt) && record.resetAt > nowMS && Number.isFinite(record.count)) {
        count = record.count;
        resetAt = record.resetAt;
      }
    } catch { /* corrupted record: start a fresh window */ }
  }
  if (count >= limit) {
    return { allowed: false, retry_after_seconds: Math.max(1, Math.ceil((resetAt - nowMS) / 1000)) };
  }
  await writeRelayState(env, key, JSON.stringify({ count: count + 1, resetAt }),
    Math.max(60, Math.ceil((resetAt - nowMS) / 1000)));
  return { allowed: true };
}

export async function createAccount(env, accountID) {
  const id = String(accountID || "").trim();
  if (!isValidAccountID(id)) return { error: "invalid account id" };
  const existing = await readRelayState(env, `${ACCOUNT_PREFIX}${id}`);
  if (existing) return { error: "account already exists" };
  const account = {
    account_id: id,
    created_at: new Date().toISOString(),
    status: "active",
  };
  await writeRelayState(env, `${ACCOUNT_PREFIX}${id}`, JSON.stringify(account), null);
  await recordDashboardEvent(env, {
    type: "audit", scope: "account", action: "created",
    account_id: id, status: "delivered", channel: "relay",
  });
  return { account };
}

export async function readAccount(env, accountID) {
  const raw = await readRelayState(env, `${ACCOUNT_PREFIX}${String(accountID || "").trim()}`);
  if (!raw) return null;
  try { return JSON.parse(raw); } catch { return null; }
}

export async function createInvitation(env, { accountID, ttlHours, purpose }) {
  const id = String(accountID || "").trim();
  if (!isValidAccountID(id)) return { error: "invalid account id" };
  const account = await readAccount(env, id);
  if (!account || account.status !== "active") return { error: "account not found" };
  const hours = Number(ttlHours);
  const ttl = Number.isFinite(hours) && hours > 0 && hours <= 24 * 30
    ? hours : DEFAULT_INVITE_TTL_HOURS;
  const code = generateInvitationCode();
  const codeHash = await sha256Hex(code);
  const record = {
    code_hash: codeHash,
    account_id: id,
    purpose: String(purpose || "").slice(0, 120),
    created_at: new Date().toISOString(),
    expires_at: new Date(Date.now() + ttl * 60 * 60 * 1_000).toISOString(),
    status: "active",
    used_by: null,
    used_at: null,
  };
  await writeRelayState(env, `${INVITE_PREFIX}${codeHash}`, JSON.stringify(record), null);
  await appendInviteIndex(env, codeHash);
  await recordDashboardEvent(env, {
    type: "audit", scope: "invitation", action: "created",
    account_id: id, invitation: codeHash.slice(0, 12),
    expires_at: record.expires_at, status: "delivered", channel: "relay",
  });
  // The plaintext code is returned exactly once and never stored.
  return { code, code_hash: codeHash, expires_at: record.expires_at, account_id: id };
}

async function appendInviteIndex(env, codeHash) {
  const raw = await readRelayState(env, INVITE_INDEX_KEY);
  let index = { hashes: [] };
  if (raw) { try { index = JSON.parse(raw); } catch { /* rebuild below */ } }
  if (!Array.isArray(index.hashes)) index.hashes = [];
  index.hashes = index.hashes.slice(-(MAX_INVITES_PER_ACCOUNT * 10) - 1);
  index.hashes.push(codeHash);
  await writeRelayState(env, INVITE_INDEX_KEY, JSON.stringify(index), null);
}

export async function listInvitations(env) {
  const raw = await readRelayState(env, INVITE_INDEX_KEY);
  if (!raw) return [];
  let index;
  try { index = JSON.parse(raw); } catch { return []; }
  const now = Date.now();
  const invitations = [];
  for (const hash of Array.isArray(index.hashes) ? index.hashes : []) {
    const rawInvite = await readRelayState(env, `${INVITE_PREFIX}${hash}`);
    if (!rawInvite) continue;
    let record;
    try { record = JSON.parse(rawInvite); } catch { continue; }
    if (record.expires_at && Date.parse(record.expires_at) <= now) continue;
    invitations.push({
      code_hash: record.code_hash,
      account_id: record.account_id,
      purpose: record.purpose,
      created_at: record.created_at,
      expires_at: record.expires_at,
      status: record.status,
      used_by: record.used_by,
      used_at: record.used_at,
    });
  }
  return invitations;
}

export async function revokeInvitation(env, codeHash) {
  const hash = String(codeHash || "").trim().toLowerCase();
  if (!/^[0-9a-f]{64}$/.test(hash)) return { error: "invalid invitation" };
  const raw = await readRelayState(env, `${INVITE_PREFIX}${hash}`);
  if (!raw) return { error: "invitation not found" };
  let record;
  try { record = JSON.parse(raw); } catch { return { error: "invitation not found" }; }
  record.status = "revoked";
  await writeRelayState(env, `${INVITE_PREFIX}${hash}`, JSON.stringify(record), null);
  await recordDashboardEvent(env, {
    type: "audit", scope: "invitation", action: "revoked",
    account_id: record.account_id, invitation: hash.slice(0, 12),
    status: "delivered", channel: "relay",
  });
  return { revoked: true };
}

// Consumes an invitation. Re-redemption by the same device is idempotent so a
// registration retry never burns a code twice. Rejections are uniform; the
// detailed reason goes to the audit log only.
export async function consumeInvitation(env, code, deviceID) {
  const supplied = normalizeInvitationCode(code);
  if (!CODE_PATTERN.test(supplied)) return { error: INVALID_INVITE_MESSAGE };
  const hash = await sha256Hex(supplied);
  const raw = await readRelayState(env, `${INVITE_PREFIX}${hash}`);
  if (!raw) return { error: INVALID_INVITE_MESSAGE };
  let record;
  try { record = JSON.parse(raw); } catch { return { error: INVALID_INVITE_MESSAGE }; }
  const expired = record.expires_at && Date.parse(record.expires_at) <= Date.now();
  const alreadyUsed = record.status !== "active";
  const sameDevice = alreadyUsed && record.used_by === deviceID && !expired &&
    record.status === "used";
  if (sameDevice) {
    // Idempotent retry: the code stays bound to this device.
    return { account_id: record.account_id, purpose: record.purpose, reused: true };
  }
  if (alreadyUsed || expired) {
    await auditRegistration(env, "invitation_unusable", deviceID, record.account_id);
    return { error: INVALID_INVITE_MESSAGE };
  }
  const account = await readAccount(env, record.account_id);
  if (!account || account.status !== "active") {
    await auditRegistration(env, "invitation_account_missing", deviceID, record.account_id);
    return { error: INVALID_INVITE_MESSAGE };
  }
  record.status = "used";
  record.used_by = deviceID;
  record.used_at = new Date().toISOString();
  await writeRelayState(env, `${INVITE_PREFIX}${hash}`, JSON.stringify(record), null);
  await recordDashboardEvent(env, {
    type: "audit", scope: "invitation", action: "consumed",
    account_id: record.account_id, device_id: deviceID,
    invitation: hash.slice(0, 12), status: "delivered", channel: "relay",
  });
  return { account_id: record.account_id, purpose: record.purpose };
}

async function auditRegistration(env, action, deviceID, accountID) {
  await recordDashboardEvent(env, {
    type: "audit", scope: "registration", action,
    device_id: deviceID, account_id: accountID || null,
    status: "rejected", channel: "relay",
  });
}

export async function approveDevice(env, deviceID) {
  const id = String(deviceID || "").trim();
  const device = await readDeviceRecord(env, id);
  if (!device) return { error: "device not found" };
  if (device.approval_status === "revoked") return { error: "device is revoked" };
  device.approval_status = "approved";
  device.approved_at = new Date().toISOString();
  if (!device.account_id) device.account_id = "default";
  await writeDeviceRecord(env, id, device);
  await recordDashboardEvent(env, {
    type: "audit", scope: "device", action: "approved",
    device_id: id, account_id: device.account_id,
    status: "delivered", channel: "relay",
  });
  return { approved: true, account_id: device.account_id };
}

// Revocation tombstones the device: sessions die at the next authenticate(),
// and the same device_id cannot register again while the tombstone lives.
export async function revokeDevice(env, deviceID, reason) {
  const id = String(deviceID || "").trim();
  const device = await readDeviceRecord(env, id);
  if (!device) return { error: "device not found" };
  device.approval_status = "revoked";
  device.revoked_at = new Date().toISOString();
  device.revoked_reason = String(reason || "").slice(0, 120);
  // Push tokens are cleared so nothing can wake the revoked client.
  device.voip_token = "";
  device.alert_token = "";
  device.watch_voip_token = "";
  device.live_activity_push_to_start_token = "";
  await writeDeviceRecord(env, id, device);
  await writeRelayState(env, `${REVOKED_PREFIX}${id}`, JSON.stringify({
    device_id: id, account_id: device.account_id || null,
    revoked_at: device.revoked_at, reason: device.revoked_reason,
  }), 365 * 24 * 60 * 60);
  await recordDashboardEvent(env, {
    type: "audit", scope: "device", action: "revoked",
    device_id: id, account_id: device.account_id || null, reason: device.revoked_reason,
    status: "delivered", channel: "relay",
  });
  return { revoked: true };
}

export async function isDeviceRevoked(env, deviceID) {
  return Boolean(await readRelayState(env, `${REVOKED_PREFIX}${String(deviceID || "").trim()}`));
}

export function deviceControlBlocked(device) {
  if (!device) return "unauthorized";
  if (device.approval_status === "revoked") return "device revoked";
  if (device.approval_status === "pending") return "device pending approval";
  return "";
}

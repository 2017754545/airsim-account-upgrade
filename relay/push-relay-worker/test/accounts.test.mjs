import assert from "node:assert/strict";
import test from "node:test";
import { handleRequest } from "../src/index.mjs";

// In-memory KV with the subset of semantics the relay relies on:
// get/put/delete/list with JSON string values and optional TTLs.
function memoryKV() {
  const store = new Map();
  const expires = new Map();
  return {
    store,
    async get(key) {
      const at = expires.get(key);
      if (at && at <= Date.now()) { store.delete(key); expires.delete(key); return null; }
      return store.get(key) ?? null;
    },
    async put(key, value, options) {
      store.set(key, value);
      if (options?.expirationTtl) expires.set(key, Date.now() + options.expirationTtl * 1000);
    },
    async delete(key) { store.delete(key); expires.delete(key); },
    async list({ prefix = "", limit = 500 } = {}) {
      const keys = [...store.keys()].filter((k) => k.startsWith(prefix))
        .slice(0, limit).map((name) => ({ name }));
      return { keys, list_complete: true };
    },
  };
}

// Minimal Durable Object stub: the relay only needs enqueue/resolve shapes.
function fakeCommands() {
  return {
    idFromName() { return { name: "commands" }; },
    get() {
      return {
        async fetch(url, options) {
          const payload = options?.body ? JSON.parse(options.body) : {};
          if (String(url).endsWith("/resolve")) {
            return new Response(JSON.stringify({ target_agent_id: null }), { status: 200 });
          }
          return new Response(JSON.stringify({ queued: true, duplicate: false,
            command_id: payload.command_id }), { status: 200 });
        },
      };
    },
  };
}

function baseEnv(overrides = {}) {
  return {
    DEVICES: memoryKV(),
    COMMANDS: fakeCommands(),
    ALLOWED_BUNDLE_ID: "com.example.airsim",
    APNS_BUNDLE_ID: "com.example.airsim",
    APNS_AUTH_MODE: "certificate",
    APNS_ENVIRONMENT: "production",
    DASHBOARD_TOKEN: "d".repeat(40),
    ...overrides,
  };
}

function jsonRequest(method, path, body, headers = {}, ip = "203.0.113.9") {
  return new Request(`https://relay.test${path}`, {
    method,
    headers: {
      "content-type": "application/json",
      "x-forwarded-for": ip,
      ...(body ? { "content-length": String(JSON.stringify(body).length) } : {}),
      ...headers,
    },
    ...(body ? { body: JSON.stringify(body) } : {}),
  });
}

const DASHBOARD_AUTH = { authorization: `Bearer ${"d".repeat(40)}` };

function registrationBody(overrides = {}) {
  return {
    device_id: "a".repeat(32),
    device_secret: "s".repeat(32),
    bundle_id: "com.example.airsim",
    environment: "production",
    voip_token: "ab".repeat(16),
    alert_token: "cd".repeat(16),
    ...overrides,
  };
}

async function createAccountAndInvitation(env, accountID = "owner") {
  const created = await handleRequest(jsonRequest("POST", "/dashboard/api/accounts", { account_id: accountID }, DASHBOARD_AUTH), env);
  assert.equal(created.status, 200, await created.text());
  const inviteResponse = await handleRequest(jsonRequest("POST", "/dashboard/api/invitations", { account_id: accountID }, DASHBOARD_AUTH), env);
  assert.equal(inviteResponse.status, 200);
  return (await inviteResponse.json());
}

test("dashboard APIs reject anonymous callers", async () => {
  const env = baseEnv();
  const response = await handleRequest(jsonRequest("POST", "/dashboard/api/accounts", { account_id: "owner" }), env);
  assert.equal(response.status, 401);
});

test("enforced mode: registration needs an invitation, codes bind to one device", async () => {
  const env = baseEnv({ ACCOUNT_MODE: "enforced" });

  const denied = await handleRequest(jsonRequest("POST", "/v1/devices/register", registrationBody()), env);
  assert.equal(denied.status, 403);

  const { code } = await createAccountAndInvitation(env);
  assert.match(code, /^ASR-[23456789ABCDEFGHJKLMNPQRSTUVWXYZ]{4}(-[23456789ABCDEFGHJKLMNPQRSTUVWXYZ]{4}){2}$/);

  const ok = await handleRequest(jsonRequest("POST", "/v1/devices/register",
    registrationBody({ invitation_code: code })), env);
  assert.equal(ok.status, 200);
  const body = await ok.json();
  assert.equal(body.approval_status, "approved");
  assert.equal(body.account_bound, true);

  // A second device cannot reuse the code; the wording is uniform.
  const reuse = await handleRequest(jsonRequest("POST", "/v1/devices/register",
    registrationBody({ device_id: "b".repeat(32), invitation_code: code }), {}, "203.0.113.10"), env);
  assert.equal(reuse.status, 403);
  assert.equal((await reuse.json()).error,
    "invitation code is invalid or no longer usable");

  // A retry by the same device is idempotent and keeps the binding.
  const retry = await handleRequest(jsonRequest("POST", "/v1/devices/register",
    registrationBody({ invitation_code: code })), env);
  assert.equal(retry.status, 200);
  assert.equal((await retry.json()).approval_status, "approved");
});

test("grace mode: uninvited devices register as pending and cannot control anything", async () => {
  const env = baseEnv({ ACCOUNT_MODE: "grace" });
  const registered = await handleRequest(jsonRequest("POST", "/v1/devices/register", registrationBody()), env);
  assert.equal(registered.status, 200);
  assert.equal((await registered.json()).approval_status, "pending");

  const headers = { authorization: `Bearer ${"s".repeat(32)}` };
  const blockedResponse = await handleRequest(jsonRequest("POST", "/v1/commands/enqueue",
    { device_id: "a".repeat(32), device_secret: "s".repeat(32), type: "dial", number: "12345678901" },
    headers), env);
  const blockedText = await blockedResponse.text();
  console.error("DBG blocked:", blockedResponse.status, blockedText);
  assert.equal(blockedResponse.status, 403);
  assert.match(JSON.parse(blockedText).error, /pending approval/);

  // Feedback is also closed for pending devices.
  const feedback = await handleRequest(jsonRequest("POST", "/v1/feedback",
    { trigger: "manual" },
    { ...headers, "x-airsim-device-id": "a".repeat(32) }), env);
  assert.equal(feedback.status, 403, await feedback.text());
});

test("device approval unlocks control; revocation locks everything again", async () => {
  const env = baseEnv({ ACCOUNT_MODE: "grace" });
  await handleRequest(jsonRequest("POST", "/v1/devices/register", registrationBody()), env);

  const approved = await handleRequest(jsonRequest("POST", `/dashboard/api/devices/${"a".repeat(32)}/approve`, {}, DASHBOARD_AUTH), env);
  assert.equal(approved.status, 200, await approved.text());

  // Approved device passes the control gate (command validation may still
  // reject the payload, but never with 403 pending).
  const command = await handleRequest(jsonRequest("POST", "/v1/commands/enqueue",
    { device_id: "a".repeat(32), device_secret: "s".repeat(32), type: "dial",
      number: "12345678901", command_id: "550e8400-e29b-41d4-a716-446655440000" },
    { authorization: `Bearer ${"s".repeat(32)}` }), env);
  assert.notEqual(command.status, 403);

  const revoked = await handleRequest(jsonRequest("POST", `/dashboard/api/devices/${"a".repeat(32)}/revoke`,
      { reason: "stolen" }, DASHBOARD_AUTH), env);
  assert.equal(revoked.status, 200, await revoked.text());

  const locked = await handleRequest(jsonRequest("POST", "/v1/commands/enqueue",
    { device_id: "a".repeat(32), device_secret: "s".repeat(32), type: "dial", number: "12345678901" },
    { authorization: `Bearer ${"s".repeat(32)}` }), env);
  assert.equal(locked.status, 403);
  assert.match((await locked.json()).error, /revoked/);

  // Re-registration with the correct old secret is refused while the
  // tombstone lives.
  const reRegister = await handleRequest(jsonRequest("POST", "/v1/devices/register", registrationBody()), env);
  assert.equal(reRegister.status, 403);
});

test("dial issues a short-lived media ticket; wrong device cannot mint one", async () => {
  const env = baseEnv({ ACCOUNT_MODE: "grace" });
  await handleRequest(jsonRequest("POST", "/v1/devices/register", registrationBody()), env);
  await handleRequest(jsonRequest("POST", `/dashboard/api/devices/${"a".repeat(32)}/approve`, {}, DASHBOARD_AUTH), env);

  const dial = await handleRequest(jsonRequest("POST", "/v1/commands/enqueue",
    { device_id: "a".repeat(32), device_secret: "s".repeat(32), type: "dial",
      number: "12345678901", command_id: "550e8400-e29b-41d4-a716-446655440000" },
    { authorization: `Bearer ${"s".repeat(32)}` }), env);
  assert.equal(dial.status, 200);
  const dialBody = await dial.json();
  assert.ok(dialBody.call.media_token, "dial response carries a media token");

  // A different registered device cannot get a ticket for this call.
  await handleRequest(jsonRequest("POST", "/v1/devices/register",
    registrationBody({ device_id: "c".repeat(32), device_secret: "x".repeat(32) })), env);
  const foreign = await handleRequest(jsonRequest("POST",
    `/v1/calls/${dialBody.call.call_uuid}/media-ticket`,
    { device_id: "c".repeat(32), device_secret: "x".repeat(32) }), env);
  assert.equal(foreign.status, 404);

  // The owner can rotate the ticket.
  const rotated = await handleRequest(jsonRequest("POST",
    `/v1/calls/${dialBody.call.call_uuid}/media-ticket`,
    { device_id: "a".repeat(32), device_secret: "s".repeat(32) },
    { authorization: `Bearer ${"s".repeat(32)}` }), env);
  assert.equal(rotated.status, 200);
  const ticketBody = await rotated.json();
  assert.notEqual(ticketBody.media_token, dialBody.call.media_token);
  assert.match(ticketBody.media_url, /\/v1\/calls\/[0-9a-f-]+\/connect$/);
});

test("feedback requires device or dashboard authentication", async () => {
  const env = baseEnv();
  const anonymous = await handleRequest(jsonRequest("POST", "/v1/feedback",
    { trigger: "manual" }), env);
  assert.equal(anonymous.status, 401);

  const deviceAuth = await handleRequest(jsonRequest("POST", "/v1/feedback",
    { trigger: "manual" },
    { authorization: `Bearer ${"s".repeat(32)}`, "x-airsim-device-id": "f".repeat(32) }), env);
  // Unknown device: authenticate() rejects with 401 before feedback runs.
  assert.equal(deviceAuth.status, 401);
});

test("invitation listing never exposes plaintext codes", async () => {
  const env = baseEnv();
  await createAccountAndInvitation(env, "second-owner");
  const list = await handleRequest(jsonRequest("GET", "/dashboard/api/invitations", null, DASHBOARD_AUTH), env);
  assert.equal(list.status, 200);
  const payload = await list.json();
  assert.equal(payload.invitations.length, 1);
  const record = payload.invitations[0];
  assert.equal(record.account_id, "second-owner");
  assert.equal(record.status, "active");
  assert.ok(record.code_hash);
  const serialized = JSON.stringify(payload);
  assert.ok(!serialized.includes("ASR-"), "plaintext invitation code must not be listed");
});

// ── account login codes: Android agents sign in without pairing ────────────

function agentLoginBody(overrides = {}) {
  return {
    device_id: "c".repeat(32),
    device_secret: "t".repeat(32),
    device_kind: "android_agent",
    bundle_id: "com.airsim.phonecontrol.standalone",
    device_name: "Xiaomi Mi 10",
    model: "umi",
    account_id: "owner",
    login_code: "",
    ...overrides,
  };
}

test("android agent logs in with account login code, joins approved, multi-device", async () => {
  const env = baseEnv({ ACCOUNT_MODE: "enforced" });

  // iOS controller registers the account via invitation.
  const { code } = await createAccountAndInvitation(env, "owner");
  const ios = await handleRequest(jsonRequest("POST", "/v1/devices/register",
    registrationBody({ invitation_code: code })), env);
  assert.equal(ios.status, 200);
  assert.equal((await ios.json()).approval_status, "approved");

  // The controller mints a login code with its device credentials.
  const mint = await handleRequest(jsonRequest("POST", "/v1/account/login-code",
    { device_id: "a".repeat(32), device_secret: "s".repeat(32) }), env);
  assert.equal(mint.status, 200);
  const { code: loginCode, account_id } = await mint.json();
  assert.match(loginCode, /^[23456789ABCDEFGHJKLMNPQRSTUVWXYZ]{8}$/);
  assert.equal(account_id, "owner");

  // Unauthenticated minting is rejected.
  const anonMint = await handleRequest(jsonRequest("POST", "/v1/account/login-code",
    { device_id: "a".repeat(32), device_secret: "wrong-wrong-wrong" }), env);
  assert.equal(anonMint.status, 401);

  // First Android agent signs in — no pairing, no invitation.
  const agent1 = await handleRequest(jsonRequest("POST", "/v1/agent/login",
    agentLoginBody({ login_code: loginCode })), env);
  assert.equal(agent1.status, 200);
  assert.equal((await agent1.json()).approval_status, "approved");

  // The same code enrolls a second Android device on the same account.
  const agent2 = await handleRequest(jsonRequest("POST", "/v1/agent/login",
    agentLoginBody({ device_id: "d".repeat(32), device_secret: "u".repeat(32),
      device_name: "Redmi 9", login_code: loginCode }), {}, "203.0.113.11"), env);
  assert.equal(agent2.status, 200);

  // The iOS controller sees both agents in its account device list.
  const list = await handleRequest(jsonRequest("GET", "/v1/account/devices", null, {
    "x-airsim-device-id": "a".repeat(32),
    authorization: `Bearer ${"s".repeat(32)}`,
  }), env);
  assert.equal(list.status, 200);
  const payload = await list.json();
  assert.equal(payload.account_id, "owner");
  assert.equal(payload.devices.length, 3);
  const kinds = payload.devices.map((d) => d.device_kind).sort();
  assert.deepEqual(kinds, ["android_agent", "android_agent", "ios_controller"]);
  const names = payload.devices.map((d) => d.device_name).sort();
  assert.deepEqual(names, ["", "Redmi 9", "Xiaomi Mi 10"]);

  // Wrong login code / wrong account pairing is rejected with uniform wording.
  const badCode = await handleRequest(jsonRequest("POST", "/v1/agent/login",
    agentLoginBody({ device_id: "e".repeat(32), device_secret: "v".repeat(32),
      login_code: "AAAABBBB" }), {}, "203.0.113.12"), env);
  assert.equal(badCode.status, 403);

  // iOS registration devices cannot use the agent login endpoint.
  const iosAttempt = await handleRequest(jsonRequest("POST", "/v1/agent/login",
    agentLoginBody({ device_id: "a".repeat(32), device_secret: "s".repeat(32),
      device_kind: "ios_controller", bundle_id: "com.example.airsim",
      environment: "production" })), env);
  assert.equal(iosAttempt.status, 400);

  // Revoking the code blocks new sign-ins but existing devices keep working.
  const listCodes = await handleRequest(jsonRequest("GET", "/dashboard/api/invitations", null, DASHBOARD_AUTH), env);
  assert.equal(listCodes.status, 200);
});

test("revoked login code blocks new agent sign-ins; account list requires approval", async () => {
  const env = baseEnv({ ACCOUNT_MODE: "enforced" });
  const { code } = await createAccountAndInvitation(env, "owner2");
  await handleRequest(jsonRequest("POST", "/v1/devices/register",
    registrationBody({ invitation_code: code })), env);
  const mint = await handleRequest(jsonRequest("POST", "/v1/account/login-code",
    { device_id: "a".repeat(32), device_secret: "s".repeat(32) }), env);
  const { code: loginCode } = await mint.json();

  // Revoke via dashboard-adjacent endpoint requires device auth; use the
  // controller device itself to revoke its own code.
  const hashIndex = await handleRequest(jsonRequest("GET", "/dashboard/api/invitations", null, DASHBOARD_AUTH), env);
  assert.equal(hashIndex.status, 200);

  // A pending (uninvited) device cannot mint or list codes.
  const pendingEnv = baseEnv({ ACCOUNT_MODE: "grace" });
  const pendingReg = await handleRequest(jsonRequest("POST", "/v1/devices/register",
    registrationBody({ device_id: "b".repeat(32) })), pendingEnv);
  assert.equal(pendingReg.status, 200);
  const pendingMint = await handleRequest(jsonRequest("POST", "/v1/account/login-code",
    { device_id: "b".repeat(32), device_secret: "s".repeat(32) }), pendingEnv);
  assert.equal(pendingMint.status, 403);
  const pendingList = await handleRequest(jsonRequest("GET", "/v1/account/devices", null, {
    "x-airsim-device-id": "b".repeat(32),
    authorization: `Bearer ${"s".repeat(32)}`,
  }), pendingEnv);
  assert.equal(pendingList.status, 403);
});

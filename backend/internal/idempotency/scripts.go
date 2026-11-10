package idempotency

// Record layout (one Redis hash per scoped key):
//
//	st    "p" in progress | "c" completed | "t" completed, response not stored (tombstone)
//	bh    body hash of the claiming request
//	rid   request id of the claiming request
//	exp   unix ms on the Redis clock when the completed record expires (first claim + TTL)
//	code  HTTP status (completed only)
//	hdr   allowlisted response headers, JSON object (completed only)
//	body  response body as replayed (completed only)
//
// Integers are written with string.format('%d') because Redis' Lua turns numbers into strings
// with 14 significant digits. Each Store builds its own *redis.Script from these sources:
// redis.Script caches the server digest, so it is mutable and not shared between stores.

// beginSrc claims a free key or reports what holds it.
// KEYS[1] record; ARGV[1] body hash, ARGV[2] request id, ARGV[3] lease ms, ARGV[4] TTL ms.
// Reply: {"f"} fresh (also when the same request id already holds the lease, so a retried
// Begin is safe) | {"x", rid} conflict | {"p", rid} in progress | {"t", rid} completed but the response was not stored | {"c", rid, code, hdr, body}.
const beginSrc = `
local cur = redis.call('HMGET', KEYS[1], 'st', 'bh', 'rid', 'code', 'hdr', 'body')
if not cur[1] then
  local t = redis.call('TIME')
  local exp = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000) + tonumber(ARGV[4])
  redis.call('HSET', KEYS[1], 'st', 'p', 'bh', ARGV[1], 'rid', ARGV[2], 'exp', string.format('%d', exp))
  redis.call('PEXPIRE', KEYS[1], ARGV[3])
  return {'f'}
end
if cur[2] ~= ARGV[1] then
  return {'x', cur[3]}
end
if cur[1] == 'p' then
  if cur[3] == ARGV[2] then
    return {'f'}
  end
  return {'p', cur[3]}
end
if cur[1] == 't' then
  return {'t', cur[3]}
end
return {'c', cur[3], cur[4], cur[5], cur[6]}
`

// completeSrc stores the result if the caller still owns the key and moves the expiry to
// first claim + TTL. KEYS[1] record; ARGV[1] request id, ARGV[2] status, ARGV[3] headers,
// ARGV[4] body. Reply: 1 stored | 0 no record (lease expired) | -1 owned by another request.
const completeSrc = `
local cur = redis.call('HMGET', KEYS[1], 'rid', 'exp')
if not cur[1] then
  return 0
end
if cur[1] ~= ARGV[1] then
  return -1
end
redis.call('HSET', KEYS[1], 'st', 'c', 'code', ARGV[2], 'hdr', ARGV[3], 'body', ARGV[4])
redis.call('PEXPIREAT', KEYS[1], cur[2])
return 1
`

// releaseSrc deletes the record if the caller owns it. KEYS[1] record; ARGV[1] request id.
// Reply: 1 deleted | 0 no record | -1 owned by another request.
const releaseSrc = `
local rid = redis.call('HGET', KEYS[1], 'rid')
if not rid then
  return 0
end
if rid ~= ARGV[1] then
  return -1
end
redis.call('DEL', KEYS[1])
return 1
`

// tombstoneSrc marks a record whose request succeeded but whose response could not be stored
// (too large for the replay limit, or the store failed): the key stays held until first claim +
// TTL, so a retry never runs the vendor call again. KEYS[1] record; ARGV[1] request id.
// Reply: 1 marked | 0 no record (lease expired) | -1 owned by another request.
const tombstoneSrc = `
local cur = redis.call('HMGET', KEYS[1], 'rid', 'exp')
if not cur[1] then
  return 0
end
if cur[1] ~= ARGV[1] then
  return -1
end
redis.call('HSET', KEYS[1], 'st', 't')
redis.call('PEXPIREAT', KEYS[1], cur[2])
return 1
`

# Phase 5 Brief: subscriptions/listen + notifyCh Delivery Fix + TER-263

Parent spec: §4.3, §5.3, §6.1, §13 (notifyCh)
Depends on: Phase 2
Size: Medium

## Objective

Server-initiated list-change notifications get their first HTTP delivery path; the client consumes them; TER-263 lazy skill loads publish `toolsListChanged`. Fixes the live bug where `NotifyResourceListChanged` fills a 16-slot buffer nothing drains on HTTP (`pkg/mcp/server/server.go:340`).

## Pinned decisions

- **[proposed] Subscription registry lives in `MCPServer`,** not the transport: `subscriptions map[string]*subscription{id, types, ch}` guarded by the existing mutex. The `subscriptions/listen` handler registers, then the streaming path (`HandleMessageStream`) holds the request open and forwards from the subscription channel. The transport stays dumb.
- **[adopted, revised in review round 2] Per-subscriber buffered channel (64); overflow terminates the subscription.** A silently dropped notification is a gap the client cannot see, so it would serve stale data until `ttlMs` expiry; terminating (HTTP: stream close; stdio: server-initiated `notifications/cancelled`) makes the documented recovery — re-subscribe and refetch, which `ttlMs` keeps cheap — actually trigger. No redelivery machinery (that was resumption).
- **[proposed] `notifyCh` is replaced, not bridged:** publishers call `s.publish(notificationType, payload)` which fans out to matching subscribers AND the legacy `Serve()` stdio path. The 16-slot `notifyCh` and its drop-when-full path are deleted.
- **Client `Subscribe` is one long-lived POST** re-opened with exponential backoff (1s..30s, jitter); on every (re)open the client refetches lists for its subscribed types before processing new events — that ordering closes the notification gap.

## Work items

1. Server: `subscriptions/listen` handler (opt-in types, `subscriptionId` assignment, tagging via `io.modelcontextprotocol/subscriptionId` in notification `_meta` — exact shape from the spec); registry + publish fanout; unregister on stream close.
2. Delete `notifyCh`; migrate `NotifyResourceListChanged` (and siblings) to `publish`.
3. Bridge: publish `toolsListChanged` when a TER-263 lazy skill load lands; wire the 15000/300000 `ttlMs` flip from Phase 3.
4. Client: `Subscribe(ctx, types...)` + demux + backoff + refetch-on-reopen; manager subscribes to `toolsListChanged` and refreshes its tool registry (replacing/augmenting any polling).
5. Request-scoped notifications (`notifications/progress`) are untouched — assert that in tests, don't reroute them.

## Test scenarios

| # | Given | When | Then |
|---|---|---|---|
| 1 | Subscriber opted into `toolsListChanged` only | Server publishes tools + resources changes | Receives only tools events, tagged with its subscriptionId |
| 2 | Two subscribers, different type sets | One publish each type | Each receives exactly its own |
| 3 | Subscriber stream closed by client | Next publish | No send to dead stream; registry entry removed; no goroutine leak (assert goroutine count) |
| 4 | Slow subscriber, >64 pending | Publishes continue | Subscription terminated (stream close / `notifications/cancelled`), Warn logged, server never blocks |
| 5 | Client `Subscribe`, server restarts | Backoff reconnect | Re-subscribes; refetches list first; a change published during the gap is reflected in the refetched list |
| 6 | Lazy skill load (TER-263) | Load lands | `toolsListChanged` published; next `tools/list` includes the tool and correct `ttlMs` |
| 7 | Streaming `loom_weave` while a listen stream is open | Weave runs | Progress events on the weave's own stream, not the listen stream |
| 8 | Legacy stdio deployment | `NotifyResourceListChanged` | Still delivered via `Serve()` loop (no regression from the notifyCh replacement) |
| 9 | HTTP deployment (the old bug) | 100 publishes, no subscribers | No warning spam, no unbounded memory; publishes are no-ops |
| 10 (race) | Concurrent publish + subscribe + unsubscribe, 50 iterations | `-race` | Clean |
| 11 (race) | Publish during subscriber disconnect | `-race` | Clean; no send-on-closed-channel panic |

## Acceptance criteria

Standing criteria, plus: scenario 3's goroutine-leak assertion uses a before/after count with settle timeout; the manager demonstrably picks up a new upstream tool without restart (integration test against a fake server).

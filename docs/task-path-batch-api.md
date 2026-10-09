# Task path-prefix batch API

The task API provides batch operations for every task type registered under
`/api/task`:

- `POST /api/task/{type}/delete_by_path`
- `POST /api/task/{type}/cancel_by_path`
- `POST /api/task/{type}/retry_by_path`

Supported task types are `upload`, `copy`, `move`, `offline_download`,
`offline_download_transfer`, `decompress`, and `decompress_upload`.

## Request

```json
{
  "path": "/storage/folder",
  "dry_run": true
}
```

- `dry_run: true` returns the matching count without altering tasks. The
  response is `{"matched": N, "processed": 0}`.
- After reviewing the count, send the same request with `dry_run` omitted
  and `expected_count: N`. If the current selection differs from N, the
  server rejects the operation with HTTP 409 and makes no changes.
- `expected_count` is optional for existing API clients; it must be
  non-negative. The guard and action use the same selected tasks, but
  running tasks may still change state while the operation executes.

`path` is normalized with the same virtual-path rules used by OpenList. Both
forward and backward slashes are accepted, repeated separators are collapsed,
and `.` / `..` segments are cleaned.

A root-wide operation is destructive and therefore requires the explicit input
`/`. If the path after user-base-path resolution is `/`, inputs such as `.`,
`./`, `//`, or other non-empty values that normalize to `/` are rejected with
HTTP 400.

For non-admin users, the normalized path is resolved against the user's base
path. A non-admin `.` may therefore legitimately mean their own base path,
not the global root. A non-admin operation can only match tasks created by
that user. Ownership filtering happens **before counting matches**, so the
response never counts another user's tasks. An admin can match tasks from
all users.

## Matching contract

A task matches when either its virtual source path or virtual destination path:

1. equals the normalized request path; or
2. is a descendant of the normalized request path.

Matching is path-segment aware: `/a` matches `/a/file`, but not `/ab/file`.

Upload tasks expose the final destination object path, including the uploaded
file name. Archive-content file and non-in-place directory tasks also include
their object name. An in-place archive directory task represents expansion into
the destination directory itself; its generated child tasks expose their final
object paths.

Local temporary paths and download URLs are not virtual storage paths and are
not considered for source-path matching.

## Operation behavior

### `delete_by_path`

Matches tasks in any state. Terminal tasks and queued tasks that have never
started are removed immediately. Active tasks are first cancelled, then the API
waits for execution to stop and failure cleanup hooks to finish before removing
them from the manager. A task that does not stop before the server-side timeout
remains visible in the manager and is not counted as processed.

The timeout is a **fixed, shared 30-second deadline for the whole request**,
not 30 seconds per matched task. Cancellation requests are issued to all
matching active tasks before waiting, so they can stop concurrently. If some
tasks remain active after the deadline, they stay in the manager and can be
inspected or deleted again later. Large requests may run into reverse-proxy
timeouts; clients should not assume a network timeout means every task was
deleted. Future asynchronous operation tracking may be useful for very large
batches.

### `cancel_by_path`

Matches only non-terminal tasks. Matching tasks are asked to cancel but remain
in the manager so their final state can be inspected.

### `retry_by_path`

Matches only tasks currently in the failed state. Matching tasks are queued for
retry.

## Response

```json
{
  "code": 200,
  "message": "success",
  "data": {
    "matched": 125,
    "processed": 124
  }
}
```

- `matched` is the number of tasks selected by the path, ownership, and state
  filters at the start of the operation.
- `processed` is the number of those tasks on which the requested state-checked
  operation was successfully applied. For deletion, it is the number confirmed
  removed from the manager after any required cancellation and cleanup.

Concurrent task transitions can make `processed` lower than `matched`. Clients
should display both values rather than treating `matched` as a success count.

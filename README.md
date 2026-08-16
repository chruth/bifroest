# bifroest

A small, single-binary Go service that connects Sonarr/Radarr to Plex/Jellyfin
in a Docker + rclone media setup, so your libraries update immediately when
Sonarr/Radarr import, upgrade, rename, or delete a file — without waiting on
Plex/Jellyfin's own filesystem change detection, which is unreliable over
rclone mounts.

## 1. What it does

```text
Sonarr/Radarr
      |
      v
   webhook
      |
      v
persistent queue (SQLite)
      |
      v
rclone mount check (anchor.bin)
      |
      v
  path mapping
      |
      v
targeted Plex/Jellyfin scan
```

1. Sonarr/Radarr POST a webhook to `bifroest` when a file changes.
2. The request is authenticated and persisted as a job, and the HTTP
   response returns immediately.
3. A background worker waits for a short delay, confirms the rclone mount
   is healthy, rewrites the path from the Sonarr/Radarr filesystem view to
   the Plex/Jellyfin filesystem view, and sends a *targeted* refresh for
   just the affected directory.
4. Failures are retried with backoff; mount outages pause processing
   without losing jobs or burning through retries.

## 2. Why it exists

In a Docker setup where Sonarr/Radarr and Plex/Jellyfin see media through
different rclone mounts, Plex/Jellyfin often doesn't notice that a new
episode or movie has appeared (or been renamed/removed), because rclone
mounts don't reliably emit filesystem change events the way a local disk
does. Sonarr/Radarr *know* exactly what changed and where — this service
takes that information and turns it into a precise, minimal library scan on
the other side, instead of you running full library scans on a timer.

It is deliberately narrow: no UI, no general automation framework, just
webhook -> queue -> mount check -> path rewrite -> targeted scan.

## 3. Architecture overview

| Package | Responsibility |
|---|---|
| `internal/config` | YAML config loading, validation, env var secret overrides |
| `internal/webhook` | HTTP handler, Sonarr/Radarr payload parsing, auth |
| `internal/rewrite` | Prefix-based path mapping and scan-directory calculation |
| `internal/queue` | SQLite-backed job queue: claiming, retries, backoff, dedup |
| `internal/mount` | Polls the rclone anchor file, tracks available/unavailable |
| `internal/target` | The `Target` interface shared by Plex and Jellyfin |
| `internal/target/plex` | Plex library discovery + targeted partial scan |
| `internal/target/jellyfin` | Jellyfin library discovery + targeted media-updated refresh |
| `internal/server` | HTTP server wiring, `/health`, `/ready` |
| `internal/logging` | Colored, human-readable `slog.Handler` for console output |
| `cmd/bifroest` | Startup wiring and graceful shutdown |

The webhook handler never talks to Plex/Jellyfin directly — it only writes
to SQLite. A small pool of background workers does all the actual scanning,
so a slow or offline Plex/Jellyfin never blocks Sonarr/Radarr's webhook
call.

## 4. Configuration

See [`config.example.yaml`](config.example.yaml) for a complete example.
Copy it to `config.yaml` and edit.

Key sections:

- `server.port` — port for bifroest's own HTTP server, always bound to all
  interfaces inside the container (there's no reason to restrict the bind
  interface here — that's what `docker-compose.yml`'s port publishing is
  for, e.g. `127.0.0.1:8080:8080` if you want it host-local only). This is
  what Sonarr/Radarr's webhook URLs and the `/health`/`/ready` endpoints
  connect to; it's unrelated to `targets.plex.url`/`targets.jellyfin.url`,
  which are addresses bifroest calls *outbound*. It must match what you
  expose in `docker-compose.yml` and use in the webhook URLs below.
- `mount.anchor` — path to the single global rclone mount-health file.
  **No Plex/Jellyfin scan is ever sent unless this file exists.**
- `queue.workers` — number of goroutines processing scan jobs concurrently.
- `queue.delay` — how long a job waits, after the triggering event, before
  it becomes eligible to run (default 5s), to give rclone time to catch
  up.
- `queue.poll_interval` — how often each worker checks SQLite for jobs
  that are now due. The queue is poll-based, so actual latency after an
  event is roughly `delay + poll_interval` in the worst case, not just
  `delay`.
- Target (Plex/Jellyfin) failures retry indefinitely with exponential
  backoff (5s, 15s, 30s, 1m, 5m, then every 5m thereafter) — there's no
  `max_retries` knob. A job never gives up permanently just because a
  target was down; it's already durable in SQLite for exactly this reason,
  and there's no UI or API to requeue a job that was marked failed, so
  giving up would mean silently losing the scan. Mount outages behave the
  same way, via `waiting_for_mount` instead of `retry`.
- `sources.sonarr.<instance>` / `sources.radarr.<instance>` — one entry per
  Sonarr/Radarr instance, each with its own `token` and its own
  `path_maps` (an ordered list of `from`/`to` prefix rewrites — see below).
- `targets.plex` / `targets.jellyfin` — `enabled`, `url`, and credentials.
- `log.level` — one of `debug`/`info`/`warn`/`error` (default `info`).
  Logs are colored (dim timestamp; green/yellow/red/blue level; an
  `error` value is always highlighted red, even on a `warn` line) —
  set the `NO_COLOR` environment variable to any non-empty value to
  turn that off, e.g. when piping logs somewhere that doesn't render
  ANSI escape codes.

### Environment variables

Every scalar setting in the config file — not just tokens — can also be
set via an environment variable, and a set env var always wins over
whatever the file has. The name is `BIFROEST_` plus the field's path,
upper-cased and joined with underscores:

```text
server.port                     -> BIFROEST_SERVER_PORT
mount.check_interval            -> BIFROEST_MOUNT_CHECK_INTERVAL
queue.workers                   -> BIFROEST_QUEUE_WORKERS
targets.plex.url                -> BIFROEST_PLEX_URL
targets.plex.token              -> BIFROEST_PLEX_TOKEN
targets.jellyfin.url            -> BIFROEST_JELLYFIN_URL
targets.jellyfin.token          -> BIFROEST_JELLYFIN_TOKEN
sources.sonarr.<instance>.token -> BIFROEST_SOURCES_SONARR_<INSTANCE>_TOKEN
sources.radarr.<instance>.token -> BIFROEST_SOURCES_RADARR_<INSTANCE>_TOKEN
database.path                   -> BIFROEST_DATABASE_PATH
log.level                       -> BIFROEST_LOG_LEVEL
```

The instance name is upper-cased (`main` -> `MAIN`, `anime` -> `ANIME`).
This is the recommended way to configure `targets.*.url`/`token`
and `sources.*.<instance>.token` for Docker Compose deployments (see
`docker-compose.yml`) — `config.example.yaml` deliberately leaves those
keys out of the file entirely rather than writing them as `token: ""`,
since an omitted key and an empty string behave identically (both fall
through to the env var) and only one of those reads as "this is meant to
be set elsewhere."

The one thing that can't be set this way is `path_maps`: a list of
`from`/`to` pairs has no sensible single-env-var form, so it stays
YAML-only. A Sonarr/Radarr instance can still be defined purely from an
env var, though — see below — just without a prefix rewrite.

### Running without a config file

`config.yaml` is entirely optional. If bifroest is started with `-config`
pointing at a file that doesn't exist (the default, `config.yaml`, included
— a missing file is not an error, only unreadable-for-other-reasons is),
it runs on defaults plus whatever `BIFROEST_*` environment variables are
set.

The one part of the config that env vars can't normally express is a
Sonarr/Radarr *instance actually existing* — its map key comes from the
file. So as a special case, any `BIFROEST_SOURCES_SONARR_<INSTANCE>_TOKEN`
or `BIFROEST_SOURCES_RADARR_<INSTANCE>_TOKEN` variable whose `<INSTANCE>`
isn't already defined in the config file creates that instance on the
spot, lower-cased (`MAIN` -> `main`), with no `path_maps` — i.e. its
source and target paths are treated as identical. This is exactly the
"same path on both sides" case from the previous section, so if that's
true for all your instances, a config file isn't needed at all:

```bash
docker run \
  -e BIFROEST_MOUNT_ANCHOR=/media/anchor.bin \
  -e BIFROEST_SOURCES_SONARR_MAIN_TOKEN=... \
  -e BIFROEST_SOURCES_RADARR_MAIN_TOKEN=... \
  -e BIFROEST_PLEX_ENABLED=true \
  -e BIFROEST_PLEX_URL=http://plex:32400 \
  -e BIFROEST_PLEX_TOKEN=... \
  -v /path/to/media:/media:ro \
  -v ./data:/data \
  ghcr.io/chruth/bifroest:latest
```

An instance that genuinely needs `path_maps` (source and target paths
differ) still needs a config file — even a minimal one with just that
instance's `path_maps`, everything else left to env vars.

## 5. Setting up the Sonarr webhook

In Sonarr: **Settings -> Connect -> + -> Webhook**

- **Notification Triggers:** enable *On Import*, *On Upgrade*, *On Rename*,
  and *On Episode File Delete*. Also enable **On Episode File Delete For
  Upgrade** (a sub-checkbox under *On Episode File Delete*) — without it,
  Sonarr silently skips the delete webhook for the old file it removes
  during an upgrade, since that's a separate trigger from plain *On
  Episode File Delete*. Optionally enable *On Series Delete* too, if you
  ever delete whole series (files included) through Sonarr. Leave the rest
  off — bifroest ignores everything else anyway, but there's no reason to
  send it.
- **URL:** `http://bifroest:8080/webhook/sonarr/main` (replace `main` with
  the instance name you gave it in `config.yaml`)
- **Method:** `POST`
- **Headers** (Advanced): add a header named `Auth` with value
  `<the token you set for this instance in config.yaml>` — just the token
  itself, nothing else.

Sonarr's built-in webhook notification doesn't have a dedicated
authentication field, but it does let you attach arbitrary headers, which
is all that's needed here.

Repeat for each Sonarr instance, pointing at its own
`/webhook/sonarr/<instance>` path with its own token.

## 6. Setting up the Radarr webhook

Same as Sonarr, under **Settings -> Connect -> + -> Webhook** in Radarr:

- **Notification Triggers:** *On Import*, *On Upgrade*, *On Rename*, *On
  Movie File Delete*, and **On Movie File Delete For Upgrade** (a
  sub-checkbox under *On Movie File Delete* — without it, Radarr won't
  send a delete webhook for the old file it removes during an upgrade).
  Optionally enable *On Movie Delete* too, if you ever delete whole movies
  (files included) through Radarr.
- **URL:** `http://bifroest:8080/webhook/radarr/main`
- **Headers:** `Auth: <radarr instance token>`

## 7. Plex configuration

Set your Plex server's base URL (e.g. `http://plex:32400`) via
`targets.plex.url` or `BIFROEST_PLEX_URL`, and a Plex
authentication token via `targets.plex.token` or
`BIFROEST_PLEX_TOKEN` — see
[Finding an authentication token](https://support.plex.tv/articles/204059436-finding-an-authentication-token-x-plex-token/).

bifroest discovers your Plex libraries and their storage locations
automatically via `GET /library/sections`. You do not need to configure
library/section IDs by hand.

This discovery happens once at startup and is cached in memory — there's
no periodic background poll, since library layouts essentially never
change once set up. If a scan path doesn't match anything in the cache
(a new library, or Plex was briefly down at startup), bifroest refreshes
the cache once on the spot and retries the match before giving up.

## 8. Jellyfin configuration

Set your Jellyfin base URL (e.g. `http://jellyfin:8096`) via
`targets.jellyfin.url` or `BIFROEST_JELLYFIN_URL`, and an API key
via `targets.jellyfin.token` or `BIFROEST_JELLYFIN_TOKEN` —
create one under **Dashboard -> API Keys**.

bifroest discovers your Jellyfin libraries via `GET /Library/VirtualFolders`
and triggers targeted refreshes via `POST /Library/Media/Updated`, the same
mechanism Jellyfin exposes for external file-watcher integrations. No
library IDs need to be configured by hand.

Like Plex, this discovery happens once at startup and is cached in memory,
with the same on-demand refresh-and-retry if a scan path doesn't match the
cache.

## 9. rclone `anchor.bin` configuration

Pick any small file that lives at a fixed path inside your rclone mount and
is *not* expected to ever disappear on its own — for example, a file you
create once with `touch /media/anchor.bin` right after mounting. It does
not need to be near your media; it's purely a canary for "is the mount
alive."

Point `mount.anchor` at that same path as seen from *inside the bifroest
container*, and mount the media volume read-only:

```yaml
volumes:
  - /path/to/media:/media:ro
```

bifroest stats this file every `mount.check_interval` (default 5s). If it
disappears, all scanning pauses (jobs are kept, not lost) until it comes
back — this correctly survives outages of hours or more.

## 10. Docker Compose deployment

```bash
cp config.example.yaml config.yaml
# edit config.yaml: path maps, target URLs, per-instance tokens (or use env vars)
docker compose up -d --build
```

See [`docker-compose.yml`](docker-compose.yml). The container only needs
**read-only** access to the media mount (for the anchor check) and a
writable `/data` directory for the SQLite database.

## 11. Path mapping examples

```text
Sonarr container sees:
/tv/Breaking Bad/Season 05/S05E01.mkv

Media mount (rclone):
/media

Plex/Jellyfin container sees:
/media/tv/Breaking Bad/Season 05/S05E01.mkv

Mapping:
/tv/ -> /media/tv/
```

Mappings are **prefix replacements only**, tried in order, first match
wins. Everything after the matched prefix is preserved verbatim. If a
`path_maps` list is configured and no entry in it matches a path,
bifroest logs the failure and records the job as `failed` rather than
guessing — it does not fall back to a full library scan.

`path_maps` itself is optional. If an instance sees its files at the same
path Plex/Jellyfin do — e.g. everything is mounted at the same absolute
path in every container, with no Docker-induced divergence to correct
for — just omit `path_maps` for that instance and the path is used
unchanged:

```yaml
sources:
  sonarr:
    main:
      # No path_maps: Sonarr and Plex see identical paths for this instance.
```

## 12. Multiple Sonarr/Radarr instances

Each instance is configured independently, with its own token and its own
path map, addressed by its own URL:

```text
/webhook/sonarr/main
/webhook/sonarr/anime
/webhook/sonarr/4k

/webhook/radarr/main
/webhook/radarr/4k
```

A request to `/webhook/sonarr/anime` is authenticated only against the
`anime` instance's token and rewritten only with the `anime` instance's
path map — instances never share credentials or mappings.

## 13. Queue / retry behavior

- **Delay:** after an event is received, bifroest waits `queue.delay`
  (default 5s) before scanning, to give rclone a moment to catch up.
- **Deduplication:** repeated events for the same target + path within that
  window (e.g. Download followed by Rename) merge into a single pending
  job instead of scanning multiple times. A job that already completed does
  not block a legitimate future scan for the same path.
- **Mount outages:** while the anchor is missing, eligible jobs move to
  `waiting_for_mount` and no target requests are sent. This does not
  consume retry attempts and survives restarts.
- **Target failures:** retried indefinitely with exponential backoff (5s,
  15s, 30s, 1m, 5m, then every 5m thereafter). There's no retry cap — a
  Plex/Jellyfin outage, however long, never causes a job to be silently
  abandoned; the error is recorded in `last_error` on every attempt.
  `failed` status is reserved for one truly permanent case: a job whose
  target was removed from `config.yaml` after the job was queued.
- **Restarts:** any job caught mid-processing by a crash/restart is reset
  to `pending` on the next startup and retried; nothing is lost because the
  queue lives in SQLite, not memory.

## 14. Troubleshooting

- **No scans happening at all:** check `GET /ready` — if it reports
  `mount_unavailable`, the anchor file isn't visible to the container.
  Check the volume mount and `mount.anchor` path.
- **`401` from the webhook endpoint:** the `Auth` header is missing or
  doesn't match the instance's configured token.
- **`404` from the webhook endpoint:** the source (`sonarr`/`radarr`) or
  instance name in the URL doesn't match anything in `config.yaml`.
- **Job stuck in `failed` with a path-mapping error:** the incoming path
  didn't match any `from` prefix in the instance's `path_maps`. Check for
  trailing-slash mismatches or a Sonarr/Radarr root folder that doesn't
  match what's configured. Unlike target failures, this is a permanent,
  immediate `failed` — fix the mapping and the *next* event for that path
  will queue a fresh job (the old one won't retry itself).
- **Plex/Jellyfin scan errors in logs:** confirm the `targets.*.url` is
  reachable from inside the bifroest container and the token/API key is
  valid. Jobs keep retrying indefinitely with backoff, so once the
  underlying problem is fixed the next scheduled attempt succeeds on its
  own — no need to restart bifroest or touch the database. Check
  `last_error` on the job (or the logs) to see what's actually failing.

## Running locally

```bash
go build ./...
go test ./...

cp config.example.yaml config.yaml
# edit config.yaml
go run ./cmd/bifroest -config config.yaml
```

## Assumptions made about external APIs

All of the following were verified directly against the Sonarr/Radarr
source (`WebhookPayload`/`WebhookImportPayload`/`WebhookRenamePayload`/etc.
in `NzbDrone.Core.Notifications.Webhook`), and cross-checked against two
independent, production Sonarr/Radarr-to-Plex/Jellyfin bridges —
[dan-online/autopulse](https://github.com/dan-online/autopulse) and the
older [Cloudbox/autoscan](https://github.com/Cloudbox/autoscan) — to make
sure the field names and scanning behavior match real-world usage, not just
the API docs.

- **Sonarr/Radarr webhooks** don't have a native authentication field;
  this relies on Sonarr/Radarr's generic custom-headers support (available
  in both apps' webhook notification settings) to send a single `Auth`
  header holding the token as-is — no `Bearer` prefix or `Authorization`
  scheme, just one header name and one value to fill in.
- **Sonarr Rename** events scan both the file's new location and its
  previous location (`renamedEpisodeFiles[].path` and `.previousPath`), so
  a rename that moves an episode into a different season folder clears out
  the stale entry in the old folder too. Both `autopulse` and `autoscan`
  do this.
- **Radarr Rename** events scan `movie.folderPath` directly rather than any
  per-file rename path — a movie normally lives in exactly one folder, so
  the folder path is already the correct, precise scan target, and this
  avoids depending on `renamedMovieFiles` being populated. Both `autopulse`
  and `autoscan` do this rather than using per-file rename data for movies.
- **Sonarr's batch "On Import Complete" event** (season-pack imports) uses
  the same `eventType: "Download"` as a single-file import but sends
  `episodeFiles` (plural) instead of `episodeFile`; both shapes are
  handled.
- **SeriesDelete**/**MovieDelete** (removing an entire series/movie, not
  just one file) scan the series/movie folder directly. Handled the same
  way in both reference projects.
- **Plex** partial/targeted scans via `GET
  /library/sections/{id}/refresh?path=...` require Plex Media Server
  1.20.0.3125 or newer.
- **Jellyfin**'s `POST /Library/Media/Updated` endpoint (used for the
  targeted refresh) resolves the owning library internally from the path;
  `GET /Library/VirtualFolders` is used separately just to validate that a
  rewritten path belongs to some configured library before sending the
  update. The update is sent with `UpdateType: "Modified"`, matching the
  convention used by both reference projects' dedicated Jellyfin targets
  (their older, separate Emby targets use `"Created"` instead — Jellyfin
  and Emby diverged enough after the fork that the two aren't always
  handled identically upstream).

## Known limitations

- Path mapping is prefix-based only — no regex, no per-file overrides.
- Only one global rclone anchor is supported, by design.
- Sonarr/Radarr `deletedFiles` produced alongside an upgrade (the old,
  replaced file, distinct from a standalone `EpisodeFileDelete`/
  `MovieFileDelete` event) are not scanned separately — only the new
  file's path is. In practice the old and new files live in the same
  directory, which is already covered.

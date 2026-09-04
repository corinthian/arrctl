# arrctl

One JSON-first CLI over the Radarr and Sonarr v3 HTTP APIs.

A dumb wrapper, pure transport. It ranks nothing, decides nothing and stores
nothing: state lives in Radarr and Sonarr, and whatever composes arrctl decides
what to fetch and which release is worth having.

```
arrctl <app> <group> <verb> [args] [--flags]

arrctl radarr movie list
arrctl sonarr queue list --page-size 50
arrctl radarr command run --name MoviesSearch --payload '{"movieIds":[37]}'
```

`<app>` is `radarr` or `sonarr`, always the first positional and always
required. There is no default app and no environment variable that supplies
one: fifteen groups exist under both names with different flags, so a caller
carrying a stale default would target the wrong instance and that instance
would answer happily.

App-independent commands sit at the root: `arrctl commands`, `arrctl config
init|show|path`, `arrctl version`.

## Install

```sh
./build.sh          # universal macOS binary in dist/
cp dist/arrctl ~/.local/bin/arrctl
```

`build.sh` runs govulncheck and the unit tests, builds arm64 and amd64, joins
them with `lipo`, signs ad hoc, and refuses to ship a binary built by a
toolchain older than go.mod's `go` directive. Deploying is a deliberate `cp`;
the build script never installs anything.

## Configuration

Resolution order per app, highest precedence first:

1. Flags: `--url`, `--apikey`, `--insecure`, `--timeout`
2. Environment: `ARRCTL_RADARR_URL`, `ARRCTL_RADARR_APIKEY`,
   `ARRCTL_RADARR_INSECURE`, the `SONARR` equivalents, and `ARRCTL_TIMEOUT`
3. `~/.config/arrctl/config.toml` (override with `--config` or `$ARRCTL_CONFIG`)
4. macOS Keychain, for the key only

The file holds URLs. Keys live in the Keychain, under service `radarrctl` /
`sonarrctl` and account `apikey`:

```sh
security add-generic-password -s radarrctl -a apikey -w <key> -U
security add-generic-password -s sonarrctl -a apikey -w <key> -U
```

```sh
arrctl config init --radarr-url http://RADARR-HOST --sonarr-url https://SONARR-HOST
arrctl config show    # resolved settings, key redacted
arrctl config path    # where the config and the keys are read from
```

`config init` writes URLs only — it never writes a key, whatever it is handed —
and refuses to overwrite an existing file without `--force`. `config show`
reports where each key came from, never its value.

`insecure = true` under an app's table skips TLS verification for that app
alone, for an instance behind a self-signed or expired certificate. Set it per
app so a working certificate on the other one is untouched.

## Output

Every command emits the same envelope on stdout:

```json
{
  "ok": true,
  "data": {},
  "meta": {
    "app": "radarr",
    "endpoint": "/api/v3/movie",
    "duration_ms": 142,
    "pagination": { "page": 1, "page_size": 20, "total_records": 168 }
  }
}
```

`meta.app` is always present, so a caller can tell the two instances apart from
the output alone.

- `--raw` emits the app's response body untouched
- `--ndjson` emits one object per line (a paged body is unwrapped to its records)
- `--terse` emits one plain-English line, the only human formatter
- `--llm` on any command emits its flags, argument shapes, output schema and
  worked examples as JSON
- `arrctl commands` emits the whole tree, each group tagged with the apps it
  exists on

Paged lists take `--page`, `--page-size`, `--sort-key`, `--sort-dir` and
`--all`. `--all` walks the pages client-side and stops at 100 with
`PAGINATION_RUNAWAY` rather than hammering the instance.

Boolean query flags take an explicit value — `--monitored true`, not a bare
`--monitored`. A toggle would leave the value as a stray positional argument.

## Errors

Errors go to stdout in the same envelope. stderr stays unused.

```json
{
  "ok": false,
  "error": {
    "code": "ARR_AUTH_FAILED",
    "message": "sonarr returned 401 Unauthorized",
    "http_status": 401,
    "hint": "security add-generic-password -s sonarrctl -a apikey -w <key> -U"
  },
  "meta": { "app": "sonarr", "endpoint": "/api/v3/series", "duration_ms": 14 }
}
```

| Exit | Meaning |
|---|---|
| 0 | Success |
| 1 | User error: bad flags, missing args, invalid config, confirm gate refused |
| 2 | The app returned non-2xx |
| 3 | Transport: DNS, TLS, timeout, refused |
| 4 | Internal arrctl error |

Codes, closed set: `BAD_CONFIG`, `BAD_REQUEST`, `CONFIRM_REQUIRED`,
`ARR_AUTH_FAILED`, `ARR_VALIDATION`, `ARR_NOT_FOUND`, `ARR_ERROR`,
`TRANSPORT_DNS`, `TRANSPORT_TLS`, `TRANSPORT_TIMEOUT`, `TRANSPORT_REFUSED`,
`PARSE_ERROR`, `PAGINATION_RUNAWAY`. One `ARR_` prefix covers both apps;
`meta.app` says which one answered. A single code-to-exit map is the only place
exit numbers live.

## Command surface

Shared by both apps, identical flags: `rootfolder`, `qualityprofile`, `tag`,
`diskspace`, `health`, `system` (status, task, restart, shutdown), `command`
(list, get, run, cancel, wait), `filesystem`, `ping`, `api-info`.

Shared with app-specific flags: `queue`, `history`, `wanted`, `release`,
`calendar`, `rename`, `manualimport`, `parse`, `rss`. The differences are all
`movieId(s)` versus `seriesId(s)`/`episodeId`/`seasonNumber` plus a few
`include*` booleans, and they live in one table.

Radarr only: `movie`, `moviefile`, `collection`.
Sonarr only: `series`, `episode`, `episodefile`, `seasonpass`, `season`.

`movie update` and `series update` merge `--payload` over a fresh `get` before
the PUT, because these APIs replace the whole resource: a partial body would
silently blank every field it omitted.

`--id-type` is per app — `tmdb|imdb` on Radarr, `tvdb|imdb|tmdb` on Sonarr —
and anything else is a `BAD_REQUEST`. Neither app has a Trakt or slug id space,
so resolve those before calling.

### Search sugar

Aliases over `command run`, so a caller never has to guess a payload shape.
Each takes `--wait` to poll the command record to completion.

| Sugar | Body |
|---|---|
| `radarr movie search --id N...` | `{"name":"MoviesSearch","movieIds":[N]}` |
| `radarr movie refresh --id N...` | `{"name":"RefreshMovie","movieIds":[N]}` |
| `radarr movie rescan --id N` | `{"name":"RescanMovie","movieId":N}` |
| `sonarr series search --id N` | `{"name":"SeriesSearch","seriesId":N}` |
| `sonarr series refresh --id N` | `{"name":"RefreshSeries","seriesId":N}` |
| `sonarr series rescan --id N` | `{"name":"RescanSeries","seriesId":N}` |
| `sonarr season search --id N --season S` | `{"name":"SeasonSearch","seriesId":N,"seasonNumber":S}` |
| `sonarr episode search --episode-ids ...` | `{"name":"EpisodeSearch","episodeIds":[...]}` |
| `<app> wanted search` | `{"name":"MissingMoviesSearch"}` / `MissingEpisodeSearch` |
| `<app> rss sync` | `{"name":"RssSync"}` |

None of these ranks or grabs on its own; the app's own automatic search does
the grabbing, under its own profile rules. That is exactly why the
library-wide ones are gated — see below.

## Confirm gates

Gated actions refuse without `--confirm` or `ARRCTL_CONFIRM=1`, returning
`CONFIRM_REQUIRED` at exit 1 and changing nothing:

every `delete`, `delete-bulk`, `editor-delete`, `bulk-delete`; `system restart`
and `system shutdown`; `manualimport submit`; `movie import` / `series import`;
`movie add` / `series add`; `queue delete*` and `queue grab*`; `release push`;
`episode monitor`; `seasonpass`; `wanted search`; `rss sync`; and `command run`
with a name outside the allowlist, because neither API document enumerates the
command-name set.

The `command run` allowlist is the targeted search/refresh/rescan names only —
`MoviesSearch`, `RefreshMovie`, `RescanMovie`, `SeriesSearch`, `SeasonSearch`,
`EpisodeSearch`, `RefreshSeries`, `RescanSeries`. The library-wide names
(`MissingMoviesSearch`, `MissingEpisodeSearch`, `RssSync`) are gated there too,
so the gate on their sugar verbs cannot be walked around by spelling the body
out by hand.

`release grab` is deliberately **not** gated. Grabbing a chosen release is the
point of the tool. The rule about disclosing a release's size before grabbing
it belongs to whatever drives arrctl, not to the binary.

Reads never gate.

## Tests

```sh
go test ./...                  # unit: envelope, exit map, config precedence,
                               # flag table, confirm gates, client mapping
go test -tags=live ./test/...  # live smoke against the configured instances
```

The live suite is read-only by construction — no grab, no mutation, no
asynchronous command dispatch of any kind — and skips itself when no instance
is configured or reachable. It resolves hosts and keys through the ordinary
configuration path; nothing is hardcoded.

## Licence

MIT.

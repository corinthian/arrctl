---
name: arrctl
description: Radarr and Sonarr control via the arrctl CLI. TRIGGER when the user wants to pull a film, search Radarr or Sonarr, check a download queue, see what is missing, add or look up a show or film, ask whether something is downloading yet, or says "/arrctl". Turns intent into arrctl commands and speaks plain English back. Retrieval only — it fetches, it does not play.
---

# arrctl

`arrctl <app> <group> <verb>` over the Radarr and Sonarr v3 APIs. Radarr is
films, Sonarr is TV. JSON in, JSON out.

<critical_notes>
- The app is ALWAYS the first word and is never optional. `arrctl radarr ...`
  or `arrctl sonarr ...`. There is no default. If you cannot tell which the
  user means, ask — do not guess, because the wrong instance will answer
  happily.
- Disclose the size before grabbing. Read `size` from the release object and
  say it in GB. Anything over 1 GB needs the user's word first. The binary does
  not enforce this; you do.
- `release grab` starts a download. So do `wanted search`, `rss sync`, and any
  search on a monitored record. Never fire one to "see what happens".
- Never pass `--confirm` on the user's behalf. A CONFIRM_REQUIRED refusal is
  the tool working: report what it would do and ask.
- arrctl never ranks releases and neither should you silently. Show the
  candidates with size, quality, seeders/age and indexer, then let the user pick.
- Trakt ids do not work here. Radarr takes tmdb/imdb, Sonarr takes tvdb/imdb/tmdb.
  Resolve first.
</critical_notes>

## Reading the output

Every call returns `{ok, data, meta}` with `meta.app` naming the instance.
Errors come back on stdout in the same shape with `{ok:false, error:{code,
message, hint}}`. Translate them; never paste raw JSON at the user.

| Exit | What to say |
|---|---|
| 0 | it worked |
| 1 | you asked for something the tool won't do as typed — read `error.hint` |
| 2 | the app refused — 401 means the key, 404 means it is not in the library |
| 3 | could not reach the instance — name which one |
| 4 | arrctl bug, or the instance sent a response arrctl could not decode |

`ARR_AUTH_FAILED` carries the exact `security add-generic-password` line in its
hint. Show that line; it is the fix.

A `DECODE_ERROR` means the instance sent something that is not the JSON it
promised, or a response over the size bound — report it, do not retry.

The full `error.code` set, by exit: 1 `BAD_REQUEST` `BAD_CONFIG`
`CONFIRM_REQUIRED` `PAGINATION_RUNAWAY`; 2 `ARR_AUTH_FAILED` `ARR_VALIDATION`
(the app rejected the payload — `message` says which field) `ARR_NOT_FOUND`
`ARR_ERROR`; 3 `TRANSPORT_DNS` `TRANSPORT_TLS` `TRANSPORT_TIMEOUT`
`TRANSPORT_REFUSED` `TRANSPORT_FAILED`; 4 `PARSE_ERROR` `DECODE_ERROR`.
Route on the code, not the message text.

Add `--terse` when you only need a one-liner. Use `jq` on the JSON for
anything you have to reason about.

## The commands worth knowing

**Is it there?**
```sh
arrctl radarr movie list | jq '.data[] | select(.title|test("NAME";"i")) | {id,title,year,hasFile,monitored}'
arrctl sonarr series list | jq '.data[] | select(.title|test("NAME";"i")) | {id,title,monitored}'
```
`hasFile:false` with `monitored:true` means it is wanted and not yet found.

**What is downloading?**
```sh
arrctl radarr queue list --include-movie true
arrctl sonarr queue list --include-series true --include-episode true
```
An empty queue with `totalRecords:0` means nothing is in flight. Check
`history list` to see whether it already finished.

**What is missing?**
```sh
arrctl radarr wanted missing --monitored true --page-size 20
arrctl sonarr wanted missing --monitored true --page-size 20
```
Booleans take a value: `--monitored true`, never a bare `--monitored`.

**Ask the app to go looking** (it grabs under its own profile rules):
```sh
arrctl radarr movie search --id N --wait
arrctl sonarr season search --id N --season S --wait
```
`sonarr series search|refresh|rescan` and `radarr movie rescan` take one id
only — several is an error, so loop over them. `radarr movie search|refresh`
and `sonarr episode search` take a list.
`--wait` polls to a terminal status and reports the outcome. On `completed` the
record's `message` says how many reports were downloaded — that message is the
honest answer to "did it get anything". A command that ends `failed`, `aborted`
or `cancelled` comes back as `ARR_ERROR` at exit 2, carrying the name, id,
status and message; read the whole record with `command get --id N`. Never tell
the user a search succeeded on the strength of the poll returning.

**Choose a release yourself:**
```sh
arrctl radarr release search --movie-id N | jq '[.data[] | {guid,title,size,quality:.quality.quality.name,indexer,age,seeders}] | sort_by(.size)'
arrctl radarr release search --movie-id N > /tmp/rel.json
jq '[.data[] | select(.guid == "GUID")][0]' /tmp/rel.json > /tmp/grab.json
arrctl radarr release grab --payload-file /tmp/grab.json --confirm
```
`release grab` needs the WHOLE release object echoed back; a bare
`{guid, indexerId}` is an `ARR_NOT_FOUND`. Take it from a fresh search — results
are cached per search and a guid from an old one 404s on its own. State the
size in GB before the grab. Wait for the user's yes above 1 GB.

**Add something new:** `movie lookup --term` / `series lookup --term` finds it
in the metadata provider; `movie add` / `series add` puts it in the library and
is confirm-gated. The add payload needs a `qualityProfileId` and a
`rootFolderPath` — read them from `qualityprofile list` and `rootfolder list`
rather than inventing them.

**Everything else:** `arrctl commands` lists every group with the apps it
exists on; `<any command> --llm` returns its flags, payload shape and examples
as JSON. Read those instead of guessing.

## Traps

- Adding a film or series as monitored with no file is a grab on the app's
  own RSS timer, whatever `searchForMovie`/`searchForMissingEpisodes` says.
  Those flags only suppress the immediate search. To add and let the user
  choose the release: add with `"monitored": false`, show `release search`
  candidates, then `movie update` / `series update` to monitored after the
  pick.
- A 4K request is a profile change, not a search flag. Radarr's automatic
  search runs under the film's current quality profile, so switching the film
  to a 2160p-capable profile comes first — and that is a `movie update`, which
  the user should authorise.
- `release grab` does not refuse a release the app marked `rejected: true` —
  it grabs it without comment. Before grabbing one, name its `rejections` to
  the user ("Radarr flagged it: below your HD profile; expected French audio")
  so the override is a decision, not an accident.
- An "Original Language (X) is wanted, but found Y" rejection reads the
  metadata provider's original language, which is often wrong for older
  European films (Donna è bello, an Italian film, is listed as French on
  TMDB). Treat it as probably bad metadata, not a bad release, and say so.
- The scanner ignores video files whose names carry no year. `manualimport
  list --folder ...` then `manualimport submit` is the tool for those, and
  submit is gated.
- `filesystem` shows files by default in arrctl. Do not conclude a folder is
  empty from a bare directory listing.
- A queue that empties on its own is normal when the download client is set to
  remove completed items. Absence from the queue is not evidence of failure —
  `history list` is.
- `--all` stops at 100 pages with `PAGINATION_RUNAWAY`. On a big history, page
  deliberately instead.
- A `--config` or `$ARRCTL_CONFIG` path that is missing or unreadable is a
  `BAD_CONFIG` at exit 1, not a quiet fall back to the default file. `arrctl
  config path` diagnoses it and still exits 0.
- A config file that will not parse is a `BAD_CONFIG` at exit 1 for every
  command, `config path` and `config init` included. Only a bad *path* is
  tolerated. Say the file has a syntax error and name it; do not retry.
- `--timeout` takes seconds as a whole number (`--timeout 30`, not `30s`).
  `command wait --timeout` is a different flag that takes Go duration syntax
  (`10m`).

## What this skill does not do

Playback. arrctl fetches; something else plays. And it holds no opinion about
what is worth fetching — that judgement is yours and the user's, out loud,
before anything is grabbed.

---

## Local files

This repo copy is canonical for everything above: generic edits land here and are copied to `~/.claude/skills/arrctl/SKILL.md`. Three files in the installed skill directory are local-only and never flow back: `PERSONAL.md` (machine-specific preferences), `LESSONS.md` (the self-improvement log). Read `PERSONAL.md` first if it exists; its rules override defaults here. If it is missing, run with the defaults.

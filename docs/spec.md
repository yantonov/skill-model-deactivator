# skill-model-deactivator — Specification

Status: ready to implement. Sections 1 to 16 hold the agreed design. Section 17 records the
closed decisions. Section 18 lists what this document excludes on purpose.

## 1. Problem

Agent runtimes discover skills from directories such as `~/.claude/skills/` and
`~/.agents/skills/`. A skill is a `SKILL.md` file with YAML frontmatter. When the
frontmatter contains `disable-model-invocation: true`, the runtime keeps the skill
available as an explicit command and hides it from automatic model selection. Pi
documents this in `docs/skills.md`.

A manual edit of this key is unreliable:

- Each tool reads a different directory and a different file pattern.
- A sync client, a package installer, or an editor can add, delete, or rewrite a skill
  directory at any time.
- A manual edit can break the YAML frontmatter or drop unrelated keys.

## 2. Goal

A background daemon that:

1. Reads a config with one thing: a list of file patterns (for example
   `~/.agents/skills/foo-bar/SKILL.md`).
2. Scans the files that match the patterns, including subdirectories.
3. Makes the frontmatter of each matched file agree with the config: every matched skill gets
   `disable-model-invocation: true`. The tool never deletes the key. A skill that no pattern
   matches is never read.
4. Repeats this work when a file changes, when a file appears, and when a file disappears.
   The watcher is the mechanism. A periodic rescan runs as a safeguard for the case where
   the watcher is not working.
5. Runs on Windows, Linux, and macOS.

Result: one file declares which skills must not be auto-invoked, and the files on disk
follow that declaration with no manual edit.

## 3. Non-goals

- Editing the skill body, the description, or any key other than
  `disable-model-invocation`.
- Removing `disable-model-invocation` from a file. The tool adds and normalises it and never
  deletes it (§7.3). Enabling a skill is a manual edit.
- Creating or repairing frontmatter. The daemon reports a `SKILL.md` without
  frontmatter and leaves it unchanged.
- Managing the installation, the update, or the package registry of a skill.
- Writing operating-system autostart entries, or documenting them. Starting the daemon is
  the user's own process configuration.
- A GUI. The daemon is a CLI plus a background process.
- Multi-user or system-wide operation. The scope is the home directory of one user.

## 3.1 Facts about the two harnesses

Read from the builds installed on this machine, not from documentation.

| Fact | Pi | Claude Code |
|---|---|---|
| Frontmatter key | `disable-model-invocation` | `disable-model-invocation` |
| Evidence | `dist/core/skills.js:262`, `dist/core/skills.d.ts:6` | binary string `If true, the model cannot invoke this via the Skill tool; only users can type the slash command.` |
| YAML parser | `yaml` 2.9 (YAML 1.2 core schema) | Bun `YAML.parse`, then a coercion helper |
| Truth test | `frontmatter["disable-model-invocation"] === true`, a strict boolean test | coerce, then `["1","true","yes","on"].includes(lower(trim))` |
| `yes`, `on`, `1` | not disabled, because the parser returns a string or a number | disabled |
| `"true"` (quoted) | not disabled, because the parser returns a string | disabled |
| `True`, `TRUE` (bare) | disabled | disabled |
| Falsy values | `false` only | `0`, `false`, `no`, `off`, and a native `false` |
| Any other value | not disabled, no warning | not disabled, no warning |
| User skill directory | `<agentDir>/skills`, which is `~/.pi/agent/skills` and not `.pi/skills`, plus `~/.agents/skills` | `~/.claude/skills` |
| Recursion | reads subdirectories at any depth | `~/.claude/skills/synced/<uuid>/<skill>/SKILL.md` is three levels deep |
| Command-only switch | `enableSkillCommands` | `skillOverrides`, with the values `on`, `name-only`, `user-invocable-only`, `off`, plus a separate `user-invocable` key |

Only a bare, unquoted `true` disables model invocation in both harnesses. Pi ignores
`"true"`, `yes`, `on`, and `1`. Claude Code accepts them. Therefore:

- The daemon writes one spelling only, the unquoted `true`. The tests show that both
  harnesses read this spelling.
- The daemon reads `true`, `True`, `TRUE`, `"true"`, `yes`, `on`, and `1` as an
  instruction to disable. It rewrites each of them to the bare `true`. This repairs a
  value that one harness would ignore.

Two related switches are outside this tool: `user-invocable`, which controls the other
direction, and Claude's `skillOverrides` setting, which lives in `settings.json` and not
in the skill file.

## 4. Decisions

| # | Question | Decision |
|---|---|---|
| D1 | How does the config say which skills to disable? | `patterns` is the only list. The tool disables every file it matches. There is no second list: to disable a skill, name its file or a glob that reaches it. |
| D2 | What happens when a skill leaves `patterns`? | Nothing. The tool never opens the file again, so the line stays. It never deletes `disable-model-invocation`, because it cannot know why the path left the list. A deletion it cannot justify is worse than a line the user can delete. |
| D2b | Which spelling goes in the file? | The bare `true` only. See §3.1. Every other truthy spelling is rewritten to this one. |
| D3 | In-place edit or whole-file rewrite? | Lines inside the frontmatter region only. Every other byte stays the same, including the end-of-line style and the BOM. |
| D4 | Where does the config live, and in what format? | One file at one constant path, `~/.skill-model-deactivator.json`, on all three operating systems. The format is JSON. Neither is configurable: no `--config` flag, no YAML, no operating-system config directory, no `XDG_CONFIG_HOME`. |
| D5 | File events or polling? | File events only. A rescan runs every 30 minutes as a safeguard for the case where the watcher is not working. It is not a second mechanism competing with the watcher. |
| D6 | Keep a backup of each edited file? | No. The edit is one line and reversible. `--dry-run` shows the planned change without writing. |
| D7 | Install an autostart entry? | No. No unit file, no plist, no scheduled task, and no command that writes one. |
| D8 | Which language? | Go. Rust is an acceptable fallback. See §14. |
| D9 | What is the command surface? | `config show` prints the effective config, `config validate` reports problems and never writes, `config create` writes the default file once, `serve` runs the daemon, `log` prints the latest log lines. |
| D10 | How are path separators handled? | A pattern may use either separator. The tool normalizes to forward slashes on read, keeps them internally, and never lowercases a filesystem pattern (§6.1). |
| D11 | Is the log level adjustable? | No. There is no level setting in the config and no flag. Every line is always emitted, and the level field is a label for the reader, not a filter (§13.3). |
| D12 | Where do log lines go? | stdout, always, and to `<state dir>/smd.log` at a constant path that is not configurable. stderr carries command failures only, never log lines (§13.3). |
| D13 | Which directories does the watcher cover? | Whatever the config lists. The code has no notion of a skill directory, so it gives no special handling to Claude's `synced/` tree, Pi's own directory, or any project path. Depth comes from the pattern (§6). |

## 5. Config

The config is one file at one constant path: `~/.skill-model-deactivator.json`. The same path
on Windows, Linux, and macOS, so one file moves between machines and lives in a dotfiles
repository with no operating-system variant. `~` means the user home directory: `%USERPROFILE%` on
Windows, `$HOME` on the other systems.

The path is not configurable and there is no `--config` flag. One file, one location, so
there is never a question about which config a running daemon read.

The format is JSON and is not configurable either. JSON has no ambiguity about `yes`, `on`,
and `true`, and every editor parses it. The tool does not accept YAML, does not look at the
file extension, and does not guess the format from the contents.

```json
{
  "patterns": [
    "~/.claude/skills/foo-bar/SKILL.md",
    "~/.agents/skills/foo-bar/SKILL.md"
  ],
  "debounce": "250ms",
  "rescan_interval": "30m"
}
```

Field by field:

| Field | Type | Default | Meaning |
|---|---|---|---|
| `patterns` | array of strings | none: the field is required | The files to keep in sync. A plain path and a glob are both accepted and equivalent in effect (see §6). `~` expands to the home directory. `*` matches one path segment, `**` matches zero or more. The user supplies this list, and a real one is usually plain paths (D13). The two paths above are an example, never an installed value. |
| `debounce` | duration | `250ms` | The window that batches file events. |
| `rescan_interval` | duration | `30m` | The interval between rescans. `0` turns the rescan off. The rescan is a safeguard, not a working path. It exists so that a watcher which is not working cannot leave the tool silently doing nothing (§9). |

A duration is a string in Go duration syntax: `250ms`, `5m`, `1h`. The unit is always
written, so it cannot be misread. Both duration keys are optional: a missing key takes the
default, so the file above names `patterns` only.

Rules:

- `patterns` has no default. The two example paths above name a placeholder skill, `foo-bar`,
  and are never installed as-is: the tool cannot know which skills a machine has. `smd config
  create` writes those two paths so the file is runnable, and the user replaces `foo-bar`
  with the skills of the machine.
- When the config file is missing, the tool uses the default `debounce` and `rescan_interval`
  and reports a warning. A run still needs at least one pattern: `smd serve` refuses to start
  with an empty `patterns` list, and so does a config the load rejected (§11.4). No command
  creates the config file except `smd config create`, which writes the default config of
  §11.3 and only when no file exists. `smd config validate` prints the same text for the
  user to paste.
- The path is always `~/.skill-model-deactivator.json`. There is no search path, no
  override flag, and no merge of several files.
- An unknown key is a config error, and the tool reports it as a `fail` finding. This
  catches a typo.
- An empty or missing `patterns` array is a config error, and `smd serve` does not run on
  one.
- `patterns` is the whole decision. The tool disables a file it matches and never reads a
  file it does not match. There is no second filter and no notion of a partial match.
- To disable one skill, list its file: `~/.agents/skills/kibana/SKILL.md`. To disable a
  family, use a glob: `~/.claude/skills/kibana-*/SKILL.md`. Both forms reach the same code.
- A pattern that matches no file is a `warn` finding in `config validate`. It is not an
  error, so a user can prepare the config before the skill exists.

## 6. Matching

A `patterns` entry is a glob, so it can be `~/.agents/skills/*/SKILL.md`. In practice a real
config is a short list of plain paths, and both forms share one code path:

```json
"patterns": [
  "~/.claude/skills/kibana/SKILL.md",
  "~/.agents/skills/kibana/SKILL.md",
  "~/.agents/skills/foo-bar/SKILL.md"
]
```

This matters for implementation scope. A glob is not an invitation to build a tree walker:
resolve a pattern to a file list, cache the list, and watch the directories those files
already sit in. A pattern with no wildcard is fully resolved at startup and never needs a
walk again. An implementation that handles wildcards correctly but assumes lists are short
tracks the usage in this document. Section 15's matcher tests are what keep the wildcard
case honest.

- `~` expands to the home directory. `%USERPROFILE%` style expansion is not supported.
- `*` matches exactly one path segment. `**` matches zero or more segments.
- The last segment of a pattern is a file name, not a directory.
- Matching uses the path after `~` expansion, with forward slashes internally.
- The tool processes a file once when two matched paths lead to the same file. The test
  is `os.SameFile` on the two `os.Stat` results, which compares device and inode on Unix
  and the file index on Windows. Do not compare text paths: that misses hard links, and
  a case-insensitive filesystem gives two spellings for one file.
- Two paths that are separate files with identical content are two targets. The tool
  edits both. Pattern roots can be copies of each other, for example a skills directory
  mirrored between two agent home directories. The tool supports that setup, and it is
  not an error. It is the reason that dedupe compares file identity and not content.
- A file that does not match a pattern is never read or written, even inside a watched
  directory.
- The tool does not follow a symlink out of the tree where the pattern matched the file.
  It reads a symlinked `SKILL.md` through the link, and the rename replaces the link with
  a regular file at that path. A skill file is not a link in practice, and one write path
  for every file is worth more than preserving an unusual link.
- `*/SKILL.md` and `**/SKILL.md` do not match a backup named
  `SKILL.md.bak.2026-10-26_17-17-25`, because the pattern ends at the file name. A pattern
  that ends in a bare `*`, such as `~/.agents/skills/*`, does match it, because every name
  matches `*`.
- Depth is a property of the pattern, not of the tool. `*/SKILL.md` reaches one level and
  `**/SKILL.md` reaches any depth, so `**` covers a tree such as
  `~/.claude/skills/synced/<uuid>/<skill>/SKILL.md` and `*` does not. The tool does not
  care what the directories mean. It handles a matched file like any other, and if a sync
  client rewrites it later, the next event or rescan reconciles it again.

### 6.1 Separators: the failure that reports nothing

The user may write a pattern with either separator. `smd config show` and `smd config
validate` accept both and normalize to forward slashes. On Windows the operating system returns paths
with backslash separators. Both `Match` and `PathMatch` in `doublestar` treat a backslash as
an escape character, so each of them returns `false` for a Windows path and no error.
Measured with `doublestar` v4.10.4:

```
pattern  C:/Users/me/.claude/skills/*/SKILL.md
path     C:\Users\me\.claude\skills\kibana\SKILL.md     ->  false   (wrong, no error)
```

A daemon that ignores this matches no file, writes nothing, and reports success. The
implementation must therefore convert both sides with `filepath.ToSlash` immediately after
`~` expansion and before any match, and keep everything internal slash-separated.
`config validate` reports a pattern that still contains a backslash at that point, and
shows the pattern with forward slashes. The tests in §15 cover this case on purpose.

Do not lowercase a filesystem pattern. On Linux `/home/me/Skills` and `/home/me/skills`
are different directories, and folding case would make the tool read and write the wrong
one.

## 7. Frontmatter edits

### 7.1 The frontmatter region

A file has frontmatter when both conditions hold:

1. Offset 0 starts with `---` and ends with LF or CRLF. The tool accepts and preserves a
   UTF-8 BOM before it.
2. A later line is exactly `---` at column 0. That is the closing delimiter.

The edited region is the set of lines between the two delimiters. An inserted line takes
the line ending of the delimiter above it, so a CRLF file stays CRLF and an LF file stays
LF. The tool copies every byte outside the region unchanged, including mixed line endings
already in the file.

### 7.2 The two fields

- `name` is a top-level scalar. The tool parses the frontmatter with
  `gopkg.in/yaml.v3` for reading only. A parse error marks the file as `error`, and the tool
  leaves the file unmodified.
- `disable-model-invocation` is the line at column 0 that matches
  `^disable-model-invocation[ \t]*:[ \t]*(.*)$`. The tool trims the value and drops the
  quotes. A line at column 0 is the only candidate, and an indented line belongs to a
  parent key or to a block scalar, so a nested key never matches.

The tool classifies each value by meaning and then writes the canonical spelling. The
matrix follows §3.1:

| Value | Meaning | Action when the skill must be disabled |
|---|---|---|
| absent | not disabled | insert |
| `true` | disabled in both harnesses | no action |
| `True`, `TRUE`, `"true"`, `yes`, `on`, `1`, `"yes"` | intended as disabled, not the canonical spelling | rewrite to the bare `true` |
| `false`, `False`, `FALSE`, `no`, `off`, `0`, `"false"` | explicitly enabled | replace with `true` |
| empty, `null`, `!!null`, any other text | not a decision | warn, no action |

To classify a value, the tool converts it to a trimmed lowercase string, drops the
surrounding quotes, and compares it against the truthy set `{true, yes, on, 1}` and the
falsy set `{false, no, off, 0}`. A value outside both sets is an error.

This matrix resolves one asymmetry. Pi tests `=== true`, so it ignores `yes` and
`"true"`. Claude Code coerces the value, so it accepts them. The bare `true` is the only
value that both harnesses accept. The falsy values are the opposite case. Pi ignores
`no`, `off`, and `false` because none of them is `true`. Claude Code reads them as an
explicit "not disabled". Both harnesses therefore leave the skill model-invocable.

### 7.3 Desired state and action

Every file reached through `patterns` must be disabled, so there is one desired state and
the planner has one column of actions:

| Current state | Action |
|---|---|
| key absent | insert `disable-model-invocation: true` as the last line of the region, before the closing `---` |
| a falsy value | replace the value with `true` in place |
| `true` | no action |
| a value outside both sets | warn, no action |
| no frontmatter | warn, no action |

The tool has no action that deletes the key. To enable a skill again, delete its path from
`patterns` and then edit the file by hand.

That asymmetry is deliberate. The tool controls one thing: a file the config still reaches.
When a path leaves `patterns`, the tool cannot tell a skill the user no longer wants from a
directory that moved, a config edited on another machine, a commented-out entry, or a
pattern that stopped matching because a parent directory changed name. In each case, deleting
the line would undo a decision the user made for a reason the tool cannot see. A stale `true`
is visible and costs one manual edit. A silently reverted one is neither.

### 7.4 Examples

Absent → disabled. The `+` marks the added line:

```yaml
---
name: kibana
description: "Search Kibana logs."
+disable-model-invocation: true
---
```

Deleted from `patterns`. The tool never opens the file, so the line stays:

```yaml
---
name: kibana
description: "Search Kibana logs."
disable-model-invocation: true
---
```

The key always goes at the end of the region. The tool never inserts it between existing
keys.

## 8. The reconcile loop

The daemon is a reconciler, not an event handler. One function does all the work. Every
file it reaches must hold `disable-model-invocation: true`, so it compares the current
frontmatter with that one desired state and writes the difference. The difference is always
one line.

```
reconcile(config):
  files = resolve(patterns)                    # glob, then dedupe by file identity
  for each file:                               # every matched file must be disabled
      plan = planFrontmatter(file, disabled)   # unchanged | patched | skipped
      if plan == unchanged:
          report "noop"
          continue
      if plan == skipped:
          warn
          continue                             # no write is attempted
      if dryRun:
          report "would-patch"
          continue
      writeFile(file, plan)                    # temp file, then rename (§10)
      report the patch
  report patterns that matched nothing
```

The planner decides. The writer only writes. Nothing compares two full buffers, because
the planner already knows whether it emitted a different line (§10).

The planner no longer reads the frontmatter `name` field, because nothing depends on it.
It parses the frontmatter only to find the `disable-model-invocation` line (§7.2).

The design gives these properties:

- **Idempotent.** A second pass plans `unchanged` for every file and writes nothing.
- **Safe against its own writes.** The daemon's write produces a watcher event. The next
  reconcile reads the file, plans `unchanged`, and stops. There is no write loop and no
  suppression logic.
- **Safe after a restart.** `serve` reconciles fully at startup, so a killed process or a
  machine that slept repairs everything on the next start.
- **Repaired by the rescan.** When the watcher is not working, whether because the
  filesystem delivers no events or because the kernel dropped one, the rescan notices the
  change within `rescan_interval` (§9). The rescan is the safeguard, and the watcher is the
  mechanism.
- **Cheap.** The tool reads only matched files, and only when they change.

### 8.1 Two locks, both in memory

The daemon keeps two in-memory locks. Neither touches the filesystem, and neither is the
process lock from §12.

**Lock 1 is the pass lock, and it is the one that matters.** A pass must not run twice at
once, because two passes over one file would plan from the same starting bytes and race on
the rename. The daemon therefore serialises passes. Hold a mutex for the whole pass, or
keep one worker goroutine that is the only caller of `reconcile()`. Either works, and the
choice does not reach the disk.

A single worker is the simpler form:

```
worker:
  for:
      wait for trigger
      clear trigger            # cleared BEFORE the pass, not after
      reconcile(config)
```

- **No two passes overlap**, because one goroutine calls `reconcile()` and nothing else does.
- **No event is lost.** A trigger that arrives during a pass sets the flag again, so the
  loop runs one more pass immediately. Clearing after the pass would drop it.
- **A coalesced trigger means "run again", not "these paths changed".** Each pass reads the
  current disk state, so the identity of the changed paths is not needed.

The follow-up pass is nearly free: the first pass already applied every change, so the
second plans `unchanged` for every file.

**Lock 2 is the per-file lock.** It exists so that one daemon never works on one file twice
at once. With a single serialised pass, that cannot happen by construction: a pass handles
each file once, and the next pass starts only after the previous pass ends. The per-file lock
is therefore a guard for the day a pass becomes concurrent, for example by adding a worker
pool to shorten a long pass. It costs one mutex per path in a small map, and it costs
nothing in correctness today.

Do not use a filesystem lock for this. A lock file per `SKILL.md` would add a file inside
every watched skill directory, which every skill scanner would then have to ignore, and on
Windows locking the `SKILL.md` itself blocks the rename that follows. The lock lives in
memory because the thing it excludes is another goroutine in the same process.

The process lock in §12 is a third, unrelated thing: it keeps one daemon per machine and is
held for the life of the process.

## 9. Watching

- For each pattern, the watch root is the longest path prefix with no wildcard. For
  `~/.claude/skills/*/SKILL.md`, the root is `~/.claude/skills`.
- The tool watches each root recursively. Where recursive watching is unavailable,
  which is the case on Windows and in parts of fsnotify on Linux, the tool adds a
  watcher for each new directory that a rescan finds.
- The tool filters events to the file names that the patterns accept (`SKILL.md`) and
  batches them over `debounce`. After the window it runs one reconcile.
- A root that does not exist produces a warning and a retry at each rescan. If the
  nearest existing ancestor receives a create event, the tool reconciles immediately.
- A watcher error, a queue overflow, or an event on a path that cannot be inspected
  schedules a full reconcile instead of a scoped one.
- Every one of these triggers sets the flag in §8.1. A rescan that fires during a pass, and
  an error that arrives during a pass, therefore coalesce into a single follow-up pass
  instead of queueing behind it.
- **The rescan is a safeguard, not a working path.** The watcher is the mechanism that
  makes the tool work. The rescan exists for one situation: the watcher is not working. That
  happens when a filesystem delivers no events at all, such as a network mount or a virtual
  path, or when the kernel drops an event because its queue overflowed. In both cases the
  daemon would otherwise sit idle and look healthy while the files on disk drift from the
  config. A full pass every `rescan_interval` closes that hole.
- The value is 30 minutes because the safeguard does not need to be fast. A skill that lags
  half an hour on a filesystem that cannot deliver events costs far less than a full pass
  every few minutes on every machine that can. Set it to `0` only when every path in
  `patterns` is on a local filesystem and you accept that a broken watcher goes unnoticed.
- A rename-over save, which VS Code and most editors perform, needs no
  special handling, because reconcile works from the file and not from an open
  descriptor.

## 10. Writes

The tool never opens a matched file for writing. That one rule is what makes a partial
update impossible, and everything below follows from it.

A file that already holds the desired state is only ever read. The tool creates no temp
file, touches no directory, tests no permission, and writes no bytes. A skill that is
already disabled costs one read per pass and nothing else, which matters because most
skills stay in the same state for months.

1. **Read and plan.** The tool opens the file for reading and streams it through the
   planner. The planner copies each line until it reaches the frontmatter, then reports the
   one line to emit in place of what is there. Before and after that line, the tool copies
   every byte and parses none.
2. **Decide.** The planner reports one of three outcomes: `unchanged`, `patched`, or
   `skipped`.
   - `unchanged` means the file already holds the desired state. The tool closes it and
     stops. The tool creates, modifies, renames, and deletes nothing, so the file's mtime
     and its directory's mtime both stay as they were. No log line appears above `debug`.
   - `patched` means exactly one line differs. The tool continues to step 3.
   - `skipped` means the file cannot be planned, for example frontmatter that does not
     parse. The tool writes one `warn` and leaves the file alone. It never tries to write.
3. **Test the target before writing.** Only now, with a patch in hand, the tool asks
   whether it can replace the file. A read-only file is a failure **only at this point**,
   because a file it does not need to patch is never tested and never reported. On Unix the
   tool tests the parent directory for write and execute permission, since the rename
   needs the directory, not the file. On Windows it relies on the rename itself, because
   file attributes do not predict the outcome.
4. **Write and replace.** The tool creates a temp file in the same directory with a name
   unique to this process and this file, streams the planned bytes into it, calls `Sync`,
   closes it, and renames it over the target in one call, with the short retry described
   below.

   The name must be unique per process. Two `smd` processes can write the same directory at
   the same time, for example a running `serve` and an operator running `smd serve --once`.
   One shared name such as `.smd-tmp` would let those two truncate each other's half-written
   file, and then both would rename it, which produces a valid file with the wrong content.
   The tool therefore uses `.<name>.<pid>-<counter>.smd-tmp`.
   The temp name never matches a pattern, because every pattern ends at the skill file name,
   so the tool cannot read its own temp file as a skill. `os.Rename` is `rename(2)` on Unix and `MoveFileExW` with
   `MOVEFILE_REPLACE_EXISTING` on Windows, so one call replaces an existing file on all
   three platforms. No build tag, no platform branch, no extra package.
5. **Report.** A completed replace is one `info` line. A failure at step 3 or step 4 is one
   `ERROR` line with the path and the operating-system error, the run continues with the
   next file, and the file counts in `failed=` in the summary.

Consequences:

- A crash can leave a stray temp file. It can never leave a truncated `SKILL.md`, because
  the original is only ever the rename target.
- The tool preserves the file mode, the BOM, and the EOL style.
- The tool deletes its own temp file on error and before it exits. A stray file from a
  killed process never matches a pattern, so the tool ignores it and the user can delete it
  at any time.
- A rename can fail because another process holds the target open. On Windows that is the
  common case rather than an unusual one, because Go opens files without `FILE_SHARE_DELETE`.
  The tool retries a failed rename up to three times over about 300 ms before it reports a
  failure. A save from an editor, a sync client, or an antivirus scan releases the handle in
  milliseconds, and the debounce window (§9) already absorbs most of these.
- The tool never falls back to writing in place. A retry that still fails is a per-file
  `ERROR`, and the next event or rescan tries the file again. Debounce means a failure here
  is rare, so an `ERROR` line means something is genuinely wrong and not that the user
  pressed save.

## 11. CLI

The binary is `smd`. The module is `github.com/yantonov/skill-model-deactivator`. The
repository host and path are an implementation detail and do not affect this spec.

`config` groups the commands that read and validate the config.

```
smd config show     [--json]
smd config validate [--json] [--strict]
smd config create
smd serve           [--once] [--dry-run]
smd log             [--lines N] [--follow] [--all]
smd help
smd version
```

There is no `--config` flag. The path is always `~/.skill-model-deactivator.json` (§5).

### 11.1 `smd config show`

Prints the effective config, which is the file contents merged over the built-in defaults,
and the two resolved paths. This answers "what is the tool actually using". It needs no file
tree, so it works when a pattern points at a directory that does not exist.

```
$ smd config show
CONFIG   ~/.skill-model-deactivator.json      (exists, 412 bytes, valid JSON)
LOG      ~/.local/state/skill-model-deactivator/smd.log   (23 KiB)

patterns          ~/.claude/skills/foo-bar/SKILL.md
                  ~/.agents/skills/foo-bar/SKILL.md
debounce          250ms (default)
rescan_interval   30m (default)
```

The tool marks a value that came from a default rather than from the file with
`(default)`. This fixture is the created file of §11.3, which holds `patterns` only, so
both durations carry the marker.

### 11.2 `smd config validate`

This command only reads. It parses and never writes, so it is safe in a pre-commit hook or
a CI step. Each finding prints one line and the summary prints the exit code.

| Finding | Reported as |
|---|---|
| The config file exists and is valid JSON | `fail` when the file does not parse, or holds an unknown key. The message names the closest known key. |
| The config schema | `fail` when `patterns` is missing or empty, or a duration does not parse. |
| Pattern syntax | `fail` when a pattern has an unbalanced bracket or brace. |
| A pattern with a backslash | `fail`, because the matcher reads a backslash as an escape and not as a separator (§6.1). The message shows the pattern with forward slashes. |
| Each pattern yields files | `warn` when a pattern matches nothing, `fail` with `--strict`. |
| A duration written as a bare number | `fail`, and the message shows the form to use: `"300"` becomes `"300ms"`. |
| Frontmatter parses | `warn` when a matched file has no frontmatter, or frontmatter that does not parse. |
| A `name` key is missing | `warn` when a matched file has frontmatter but no `name`. |
| Every pattern matches a file | `warn` when a pattern matches nothing, with the text `stale`. |
| Watch roots exist | `warn` when a root directory is missing. |

```
$ smd config validate
config       ~/.skill-model-deactivator.json (exists, 412 bytes, valid JSON)
patterns     3 patterns, 78 files
log          ~/.local/state/skill-model-deactivator/smd.log (23 KiB)

ok           config parses
ok           patterns yield files
ok           frontmatter parses in all matched files
warn         ~/.agents/skills/bl/SKILL.md: no name key in frontmatter
warn         pattern "~/.agents/skills/stale-skill-*/SKILL.md" matches nothing: stale

2 warnings, 0 failures
```

Each finding prints a suggested change where one exists, and the tool makes none of them.
Copy the suggestion into the file, or run `smd serve --once` to apply the frontmatter ones,
which is the same reconcile the daemon performs (§8).

It exits `0` when every finding is `ok` or `warn`, and `1` when any finding is `fail` or the
config cannot be read at all. With `--strict`, a `warn` also gives `1`. When no config file
exists, it prints an example to paste and exits `1`.

### 11.3 `smd config create`

Bootstraps the config file. It writes the text below and nothing else: no state directory,
no log, and no other file.

```json
{
  "patterns": [
    "~/.claude/skills/foo-bar/SKILL.md",
    "~/.agents/skills/foo-bar/SKILL.md"
  ]
}
```

This is the same text `config validate` prints when the file is missing, so a user reads one
config and not two. The patterns name two skills instead of a glob, so the file never reads
as "disable everything": `foo-bar` is a placeholder to replace with the skills of the
machine. `debounce` and `rescan_interval` are left out, because the defaults of §5 apply to
a missing key and `config show` reports both as `(default)`.

The created file is a runnable config: it parses, and a pattern that matches no file is only
a `warn` (§5), so `config validate` exits 0 on a fresh machine before any skill matches.

```
$ smd config create
created ~/.skill-model-deactivator.json

{
  "patterns": [
    "~/.claude/skills/foo-bar/SKILL.md",
    "~/.agents/skills/foo-bar/SKILL.md"
  ]
}
```

When the file already exists, the command writes nothing and prints one error line naming
the path, then exits `1`. It never overwrites a list the user wrote.

### 11.4 `smd serve`

Starts the daemon. It runs a full reconcile at start, which catches up on changes made while
the daemon was down, and then watches. `--once` runs one reconcile and exits, for cron or
for a test. `--dry-run` prints the planned actions and writes nothing.

`serve` parses the config once, at startup, and holds the result in memory for the life of
the process. It never opens the config file again: not on each pass, not on each rescan, not
on a signal. A file modification has no effect until the process restarts.

In practice: edit the config, and the running daemon keeps using the values it started
with, however many hours ago. To apply an edited
config, restart the daemon. For a one-off apply with the new values, use
`smd serve --once`, which parses the file fresh.

Because the tool opens the file exactly once, no half-written config can ever reach a
running process, and no parse error can appear part-way through a run.

`serve` exits `0` when it stops normally or when another instance already holds the lock, and
`1` when the config cannot be read or cannot drive a pass: an empty `patterns` list, an
unknown key, or a file that is not JSON all give one stderr line and exit `1`, for every form
including `--once` and `--dry-run`. A refused run takes no lock, because nothing is running. It
writes one `ERROR` line with the reason to `<state dir>/smd.log`, creating the state directory
and the log if they are missing: a failed bootstrap is the case where the reason matters most,
and `smd log` is where a user looks for it. A log that stops at the last run of an older
process reads as a tool that never started. A per-file write failure does not change the exit
code: the daemon keeps running and reports it in the log (§10).

### 11.5 `smd log`

Prints the last lines of the log file at `<state dir>/smd.log` (§13.3).

```
smd log [--lines N] [--follow] [--all]
```

- `--lines N` prints the last N lines. The default is 10. `--lines` with no value is an
  error, and so is `0` or a negative number.
- `--follow` prints the last N lines and then keeps printing new ones as they arrive.
- `--all` prints the whole file, including `smd.log.1` if it exists, oldest first. This is
  the one way to see across a rotation.

The tool reads the file directly, so it works whether or not the daemon runs, and it needs
no state. It prints the resolved path on the first line, so a reader always knows which file
these lines came from.

`smd log` reads the past. To watch the present, run `smd serve` and read its stdout, or use
`--follow` here when the daemon writes a file.

```
$ smd log
~/.local/state/skill-model-deactivator/smd.log (last 10 of 4 812 lines)

2026-11-02T10:11:58.004Z DEBUG path=~/.agents/skills/bl/SKILL.md desired=true action=none result=noop
2026-11-02T10:12:03.881Z INFO  path=~/.agents/skills/kibana/SKILL.md desired=true reason=absent action=inserted result=ok
2026-11-02T10:12:04.317Z INFO  watched=78 changed=1 wrote=1 skipped=0 failed=0  reconcile finished duration=41ms
```

Behaviour in the cases that matter:

| Case | Result |
|---|---|
| The file does not exist | The tool says so on stderr, names the path, and exits `1`. |
| `--lines N` larger than the file | Prints the whole file and does not complain. |
| The daemon writes to the file while `smd log` reads it | Prints the last complete lines. The tool drops a trailing partial line. |
| `--follow` and the file rotates | Reopens `smd.log` after the rename and keeps printing, so a rotation does not end the stream. |
| `--follow` and Ctrl-C | Exits `0`. |

`smd log` never writes, never truncates, and never rotates. It reads.

### 11.6 `smd help`

The two paths are the first thing a user needs and the hardest to guess, so they come first:

```
$ smd help
smd - keep skill frontmatter in sync with a declared list of patterns

CONFIG   ~/.skill-model-deactivator.json      (exists, 412 bytes, valid)
LOG      ~/.local/state/skill-model-deactivator/smd.log   (23 KiB)

COMMANDS
  config show       Show the effective config and both paths.
  config validate   Check the config and the matched files. No writes.
  config create     Write the default config. Refuses an existing file.
  serve             Start the daemon: reconcile now, then watch for changes.
  log               Print the last log lines. --lines N, --follow, --all.
  help              Show this text.
  version           Show the build version.

OPTIONS
  --dry-run   Report the planned changes and write nothing.
  --once      Run one reconcile and exit.
  --json      Emit machine-readable output (config show, config validate).
  --strict    Treat a warning as a failure (config validate).
  --lines N   Number of log lines to print (log). Default 10.
  --follow    Keep printing new log lines (log).
  --all       Print the whole log, including the rotated file (log).

The config path is always ~/.skill-model-deactivator.json. There is no --config flag.
```

## 12. Daemon lifecycle

- **Single instance.** The daemon holds an exclusive lock file in the state directory. A
  second `serve` exits with code 0 and the message "already running (pid N)". The operating
  system releases the lock on exit, including after a crash, because the lock is an
  operating-system file lock and not a bare pid file.
- **State directory.** The directory holds one lock file and the log, both at constant
  paths.
  `%LOCALAPPDATA%\skill-model-deactivator\` on Windows,
  `~/.local/state/skill-model-deactivator/` on Linux, and
  `~/Library/Logs/skill-model-deactivator/` on macOS. It holds `lock` and `smd.log`
  (§13.3). Neither path is configurable. It holds no running state, because the daemon
  keeps none: the files on
  disk are the state and the log is the history. The config is never here. Deleting
  the directory costs the log and nothing else.
- **Signals.** On SIGINT or SIGTERM the daemon stops watching, finishes the current file,
  releases the lock, and exits with code 0. On Windows it handles the
  console control events.
- **Lifecycle lines.** One run writes, in order, `startup`, `reconcile started`,
  `reconcile finished`, `serving`, then one pass per trigger or rescan, and `shutdown`. The
  `serving` line marks the moment the watcher is live, and a run that refuses to start writes
  one `ERROR` line with the reason before it exits 1 (§11.4, §13.2).

## 13. Logging

Every line carries a timestamp. No message depends on the reader knowing when it ran.

### 13.1 Line format

```
2026-11-02T10:12:04.317Z INFO  watched=78 changed=1 wrote=1 skipped=0 failed=0  reconcile finished duration=41ms
2026-11-02T10:12:04.301Z INFO  path=~/.agents/skills/kibana/SKILL.md desired=true reason=absent action=inserted result=ok
2026-11-02T10:12:04.302Z DEBUG path=~/.agents/skills/bl/SKILL.md desired=true action=none result=noop
2026-11-02T10:12:04.305Z ERROR path=~/.agents/skills/locked/SKILL.md action=inserted result=failed error="permission denied"
```

A failed write is an `ERROR` line, one per file, in the same `key=value` shape as every
other line. The `path` comes first so `grep ERROR | grep path=<file>` finds it, and the tool
quotes the `error` value because it contains spaces.

The order is timestamp, level, then `key=value` pairs. A stable shape means `grep`,
`awk`, and `jq`-style parsing work without a parser for the prose. Timestamps are UTC in
RFC 3339 with milliseconds. A user who reads a copied log has the exact time of each
change.

### 13.2 What the daemon logs

The level field is a label for the reader. The daemon always emits every level:

| Level | Events |
|---|---|
| `info` | Startup with the resolved config path, log path, and watched roots. Every reconcile start and finish, with the file counts and the duration. Every write, with the path, the desired state, the reason, the action, and the result. `serving`, once the watcher is live. Every shutdown. |
| `debug` | The decision for each file, including the ones that need no change. Each watcher event, with the path and the event kind. Debounce ticks. |
| `warn` | A pattern that matches no file. A watch root that does not exist. A matched file without frontmatter, or with frontmatter that does not parse. A value outside both the truthy and the falsy sets. |
| `error` | One line per failed write, with the path, the action the tool tried, and the operating-system error. A lost lock. A reconcile that stops on an unexpected error. A run that refused to start, with the reason. Every failure is an `error` line and none is only a `warn`. |

Every write is an `info` line with its own timestamp, so the history of one file can be
recovered with `grep path=<file>`.

The order of the lifecycle lines is the reading order of one run: `startup`, `reconcile
started`, `reconcile finished`, `serving`, then one pass per trigger or rescan, and
`shutdown`. A log that ends on `serving` is a daemon that is watching and has done no work
since, so a daemon that writes nothing after it looks different from a daemon that never
started.

### 13.3 Log destination

- The daemon emits every line it produces, always. There is no level in the config and no
  flag, so nothing can silence a line. A daemon that writes nothing looks the same as a
  daemon that is doing nothing, and the log is the only evidence of work.
- The level field in each line is a label, not a filter. It tells the reader what kind of
  event the line records, so a script can select on it: `smd log --all | grep ' ERROR '`.
  The tool itself never drops a line on a level test.
- Log lines go to **stdout**, so a pipeline such as `smd serve | tee smd.log` or
  `smd serve | grep ERROR` works with no special handling. The tool treats a terminal, a
  pipe, and a file the same way.
- Log lines also go to one file at one constant path, `<state dir>/smd.log` (§12). The path
  is not configurable and the file is always written, so the daemon keeps a record with no
  configuration at all. Monitoring through stdout and keeping a durable
  record are both available at the same time.
- The tool creates the state directory on first write if it is missing.
- `stderr` carries only command failures: a config that does not parse and a file that
  cannot be written. Log lines never go to stderr, so `2>/dev/null` silences diagnostics
  and never hides the log.
- The file rotates at 1 MiB. The tool keeps one old file, named `smd.log.1`. Rotation is
  logged before it happens, so the old file does not end mid-event without a record.
- `smd log` reads this file (§11.5). It prints the last 10 lines by default, takes
  `--lines N`, and can follow the file or print everything including the rotated file.
- The tool never logs file contents.

## 14. Implementation plan, Go

Module: `go 1.22+`.

| Dependency | Use |
|---|---|
| `github.com/fsnotify/fsnotify` | file events on all three operating systems |
| `github.com/bmatcuk/doublestar/v4` | `*` and `**` matching |
| standard library `encoding/json` | the config |
| `gopkg.in/yaml.v3` | frontmatter scalars only. The config is JSON, so the standard library handles it. |

Layout:

```
cmd/smd/                main, command dispatch
internal/config/        load, defaults, validation
internal/match/         ~ expansion, glob resolution, dedupe by file identity
internal/frontmatter/   detect the region, read the fields, plan the edit (pure)
internal/reconcile/     the reconcile loop, the plans, dry-run, the summaries
internal/watch/         roots, recursive watchers, debounce
internal/daemon/        lock, logging, signals
internal/platform/      the state and log directories per OS
docs/spec.md
```

A Rust fallback would use `notify`, `globset` or `wax`, `serde_yaml`, `clap`, `fs2` for
the lock, and `directories`. The design is the same. `std::fs::rename` performs the atomic
replace on all three platforms, as in §10.

## 15. Tests

Unit tests, table-driven:

1. Frontmatter detection: no frontmatter, frontmatter only, a BOM, CRLF, `---` inside the
   body, frontmatter that never closes.
2. Config load. Each case is one test:
   - A valid `.json` file.
   - A YAML file at the config path, which must fail with a message that names JSON.
   - An unknown key.
   - `patterns` missing or empty.
   - A missing file, which must give the `debounce` and `rescan_interval` defaults, an empty
     `patterns`, and a warning.
   - A valid duration.
   - An invalid duration.
3. Edit planning: absent → insert. Each truthy non-canonical value (`yes`, `on`, `1`,
   `"true"`) → rewrite to the bare `true`. Each falsy value (`false`, `no`, `off`, `0`,
   `"false"`) → replace with `true`. A canonical `true` → no change. An empty value
   and an arbitrary value → warn. A key nested under another key → treat as nested and
   ignore. Text that looks like the key but sits indented, for example inside a `|` block
   scalar → ignore, because the pattern anchors at column 0. The same text at column 0 is a
   real key, so the tool handles it like one.
4. Byte preservation: each case above round-trips with only the intended bytes changed.
   The tool keeps the EOL, the BOM, and the file mode.
5. Matcher. Each case below is one test with a stated expected value:
   - `*` matches one segment and not two. `**` matches zero segments and many:
     `**/SKILL.md` matches `SKILL.md`, and `skills/**/SKILL.md` matches
     `skills/SKILL.md`.
   - A Windows path with backslashes matches after `filepath.ToSlash` on both sides.
     The same path without conversion must be asserted as **not** matching, so the guard
     cannot be deleted without a test failure.
   - `*/SKILL.md` rejects `SKILL.md.bak.2026-10-26_17-17-25`, and `*` accepts it;
     `*.md` rejects it as well.
   - A lowercase pattern does not match a mixed-case directory name, because the tool does
     not fold case.
   - Dedupe: two `os.Stat` results for one file give `os.SameFile == true` and the file is
     processed once. Two files with equal content give `false`, so the tool processes both.
6. Patterns are the decision: the planner marks a file matched by any pattern as `disabled`,
   and the tool never opens a file matched by none. A skill whose frontmatter `name` differs
   from its directory needs no special handling, because nothing reads `name`. A pattern that
   resolves to no file produces one `warn`.
7. Value matrix from §3.1: for each of `true`, `True`, `TRUE`, `"true"`, `yes`, `on`,
   `1`, the planned output contains exactly the unquoted
   `disable-model-invocation: true`. For `false`, `no`, `off`, `0`, `"false"`, the output
   is the bare `true`. For an empty value and for arbitrary text, nothing changes and the
   tool produces a warning.
8. Idempotency: a plan applied to its own output produces no change.
9. Log lines: each line starts with an RFC 3339 UTC timestamp with milliseconds, then
   the level, then `key=value` pairs. The tool quotes a value that contains a space. A
   write has `path`, `desired`, `reason`, `action`, and `result`. Rotation happens at
   1 MiB and keeps exactly one file, `smd.log.1`.
10. Findings from §11.2, one test per finding, each with a fixture that must produce `ok`,
    `warn`, and `fail`:
    - an unknown key, naming the closest known key in the message.
    - `patterns` empty and `patterns` missing.
    - a duration of `"300"`, which `validate` reports with `"300ms"` as the suggestion.
    - a pattern with a backslash, which `validate` reports with the forward-slash pattern.
    - a pattern that matches nothing.
    - frontmatter that does not parse, and frontmatter with no `name`.
    - a pattern that matches no file, reported as stale.
    - a missing watch root, and a missing state directory.
11. `log` behaviour, on a fixture file of known content:
    - The default prints exactly the last 10 lines.
    - `--lines 1` prints one line, `--lines 10000` prints the whole file without an error.
    - `--lines 0`, `--lines -1`, and `--lines` with no value are errors.
    - A file with fewer than 10 lines prints every line and no error.
    - A trailing partial line, with no closing newline, is not printed.
    - With the log file absent, the tool writes one stderr line and exits `1`, and prints
      nothing on stdout.
    - `--all` concatenates `smd.log.1` then `smd.log`, oldest first.
12. No level filter: a debug line written by the daemon appears in the output with no
    configuration change and no flag. There is no code path that suppresses a line.
13. Log destination, with two fixtures:
    - stdout points at a terminal.
    - The shell pipes stdout.

    Each case asserts that a log line reaches stdout, and that the same line reaches
    `<state dir>/smd.log`, so the tool writes the file whether or not stdout points at a
    terminal.
14. Stream discipline: a log line never goes to stderr, and a `validate` finding never goes
    to stdout. Assert both directions, so a refactor cannot quietly merge the two streams.
15. An already-disabled file stays untouched. Snapshot the file content, its mtime, the
    directory listing, and the directory mtime. Run a pass where every matched file is in
    the desired state. Assert that all four are identical afterwards, that no `.smd-tmp`
    appeared even for an instant, and that a read-only file in the same set produces no
    `ERROR` and no `warn`.
16. Read-only file that needs a patch, flipped from the same fixture:
    - Exactly one line at level `ERROR` names that path, its action, and the
      operating-system error. No `warn` reports the same failure.
    - The summary line counts the file in `failed=1` and not in `wrote=`.
    - The other matched files in the same batch are still written.
    - The read-only file is byte-identical to before the run.
    - A second reconcile with the file still read-only produces the same single `ERROR`
      line, which proves the run retries rather than giving up permanently.
    - `smd serve --once` reaches this result through the planner and writer in §10, and a
      second pass produces the same single `ERROR`.
17. Rename retry, using a fixture that fails the first two renames and succeeds on the
    third. The tool writes the file, logs one `info` line, and logs no `ERROR`. With all
    three renames failing, the tool logs one `ERROR`, leaves the original byte-identical,
    leaves no `.smd-tmp`, and counts the file in `failed=`.
18. Single config read. Start `serve` against a valid config, overwrite the file with one
    that would match nothing, then force a reconcile. The pass must still use the values
    from startup, and the file must not be opened again. Then replace the file with
    unparseable bytes: the daemon keeps running and never logs a parse error.
19. Concurrency, in two parts:
    - Two triggers arrive while a pass is running. Assert one pass completes and exactly one
      follow-up pass runs, and that no pass overlaps another (a counter incremented on entry
      and decremented on exit never exceeds one).
    - With the per-file lock exercised directly, two goroutines asking for the same path
      serialise, and the second sees the result of the first.
20. `validate` never writes. Run it against a fixture that has every finding at once and
    assert that no file on disk changed, including the config, and that no temp file is
    left behind.

Integration tests on a temp directory tree. One run disables the listed skills. A second
run writes nothing. The daemon picks up a skill directory created after start. The daemon
follows a disabled skill that renames itself. A file deleted from the tree changes the
report only.

Manual acceptance steps, one run per operating system.

| # | Test |
|---|---|
| A1 | With no config file, `smd config validate` prints the example patterns, the file count, and the resolved config path `~/.skill-model-deactivator.json`; `smd config show` prints `patterns          (empty)`, because the example is never an installed value. `smd serve` on the same home exits 1 with one stderr line. |
| A1b | A `.json` file is the only accepted format. A YAML file at the same path fails to parse, and the error names JSON as the expected format. |
| A1c | On Windows, `smd config validate` reports a non-zero file count. A count of zero is the separator bug from §6.1, and it is the first thing to look at on a new platform. |
| A1d | With no config file, `smd config create` writes `~/.skill-model-deactivator.json` holding the two `foo-bar` patterns and no duration, creates no state directory, and exits 0. A second run exits 1, names the path, and leaves the file byte-identical. `smd config show` then reports `debounce` and `rescan_interval` as `(default)`. |
| A2 | `smd serve --once --dry-run` on a real skills tree prints only the planned changes, and the tree stays unchanged. |
| A2b | With every matched skill already in the desired state, `smd serve --once` writes no bytes. File mtimes and directory mtimes are identical before and after, and no `.smd-tmp` file appears anywhere. |
| A3 | `smd serve --once` adds `disable-model-invocation: true` to exactly the disabled skills, and the diff shows one added line per file. |
| A4 | After the user deletes one path from `patterns`, the reconcile leaves that file alone and restores the others to their original bytes. |
| A4b | The daemon rewrites a hand-written `disable-model-invocation: yes` to the bare `true` in one pass, and the diff shows one changed line. |
| A4c | A skill listed as an exact path and a skill reached by a glob both end up disabled. The two pattern forms reach the same code. |
| A5 | A manual edit of a matched `SKILL.md`, while the daemon runs, triggers a reconcile within 1 s and leaves the file in the desired state. |
| A6 | The daemon picks up a skill directory created while it runs within 1 s. |
| A7 | When two `smd serve` processes start, the second exits with code 0 and the message "already running". |
| A9 | A read-only `SKILL.md` produces an error line and does not stop the daemon. |
| A10 | `smd help` prints the resolved config path with its state and the resolved log path with its size, before the command list. |
| A11 | `smd serve` writes one `info` line per change, each with a timestamp, so `grep path=<file> smd.log` returns the full history of that file in order. |
| A12 | `smd config show` prints every field with its effective value, marks the `debounce` and `rescan_interval` defaults, prints `(empty)` for a missing `patterns`, and shows both paths. `--json` gives the same data as one object. |
| A13 | `smd config validate` on a clean tree exits 0. Add an unknown key and it exits 1 naming the closest known key. |
| A14 | `smd config validate` on a dirty tree exits 1, writes nothing, and no file on disk changes. |
| A15 | `smd config validate` prints a suggested value for each repairable finding, and applies none of them. |
| A16 | A pattern written with backslashes still matches, and `smd config validate` shows the forward-slash pattern in its finding. |
| A17 | `smd log` prints 10 lines with the path on the first line. `smd log --lines 3` prints 3. `smd log --all` includes the rotated file. |
| A18 | `smd log` reads the file while the daemon runs, and `smd log --follow` shows a new write within 1 s. With the log file deleted it exits 1 and says so on stderr. |
| A19 | `smd serve` shows log lines on stdout and writes the same lines to `<state dir>/smd.log`. Piping `smd serve` into `grep ERROR` filters log lines with no special flags. |
| A20 | With `2>/dev/null`, a full run still shows every log line, which proves nothing logs to stderr. With `1>/dev/null`, the file still receives every line. |
| A21 | Make a matched `SKILL.md` read-only that already holds the desired state. `smd serve --once` reports nothing for it. Then change the config so it needs a patch. The run writes every other file, writes one `ERROR` line naming it, counts it in `failed=1`, and leaves its bytes untouched. |
| A22 | Start `smd serve`, then delete one path from `patterns`. The daemon keeps the startup values, so that skill stays disabled. Restart it and the skill is no longer touched. |
| A23 | `smd serve` writes one `serving` line after the startup pass and before the first pass that a trigger causes, so a log that ends on that line is a daemon that is watching and has done nothing since. |
| A24 | With a broken config, `smd serve` exits 1 with one stderr line and writes one `ERROR` line carrying the reason to `<state dir>/smd.log`. On a home that never ran the daemon it creates the state directory and the log and exits 1, and it takes no lock. |

## 16. Milestones

| M | Scope | Done when |
|---|---|---|
| M1 | config load, matcher, frontmatter planner, `smd config show`, `smd config validate`, `smd config create`, `smd serve --once --dry-run` | the unit tests pass, and A1, A1b, A1c, A1d, A2, A2b, A12, A13, and A16 pass |
| M2 | atomic writes, `smd serve --once`, idempotency, failure accounting | A3, A4, A14, A15, and A21 pass |
| M3 | watchers, debounce, rescan safeguard, lock, logging to stdout and file, `smd log`, `smd help` with the two paths | A5, A6, A7, A9, A10, A11, A17, A18, A19, A20, and A22 pass |

## 17. Closed points

No open questions remain. This section records the settled points, so a later reader does
not reopen them.

- **The daemon applies a diff and nothing else.** No mode of operation and no policy switch.
  For each matched file the daemon computes the difference between the current frontmatter
  and the desired state and writes it. The difference is always one line (§7.3).
  §8 shows the loop.
- **The tool adds the key and never deletes it.** The direction is one-way by design: the
  tool acts only on files the config still reaches, so a path leaving `patterns` ends the
  tool's interest in that file. It cannot tell whether the user no longer wants the skill,
  whether the directory moved, or whether the config came from another machine. An
  unjustified deletion would undo a decision the user made (§7.3).
- **The config lives in memory for the life of the process.** `smd serve` parses
  `~/.skill-model-deactivator.json` once at startup and holds the parsed values in memory. It
  never re-reads the file, so there is no reload path: no watcher on the file, no signal
  handler, no re-parse between passes. To apply an edited config, restart the daemon.
  `smd serve --once` is the fast path for one run with new values. This is also why a
  half-written config cannot reach a running process (§11.4).
- **Binary name.** `smd`.
- **Module path.** `github.com/yantonov/skill-model-deactivator`. The repository host is an
  implementation detail.

Earlier decisions, also closed:

- One list decides everything: `patterns`. The tool disables a file it matches and never
  opens a file it does not match (D1).
- No allowlist. The only control is the explicit list of skills to disable.
- No project-local patterns. The user declares each path at the user level. The tool
  manages a project tree only when the user adds its path to `patterns`.
- A synced or mirrored directory gets no special handling. Two separate files with equal
  content are both targets. Dedupe compares file identity, not content.
- The tool ships no opinion about which directories to watch. The default `patterns` are an
  example, and the user supplies the config. Including Pi's own `~/.pi/agent/skills`,
  Claude's `synced/` tree, or a project directory is a config decision, not an
  implementation one.

## 18. Deferred

- `smd toggle <name>`, a shortcut that edits the config file and triggers a reconcile,
  so the user never opens the config by hand. It is a config editor and not a watcher
  feature, and it changes nothing in §8.




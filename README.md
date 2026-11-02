# skill-model-deactivator

Keeps a declared list of skills hidden from automatic model selection, and re-applies that
list every time something else rewrites the skill files.

## Why this exists

Skill directories are not yours alone. Agent installers, plugin registries, and package
update commands write into `~/.claude/skills`, `~/.pi/agent/skills`, and `~/.agents/skills`,
and they re-sync those files on the next update.

Every installed skill loads its name and description into the model's context on every
turn, and competes for the moment when the model decides which skill to reach for. Two
problems follow.

**1. Duplicates compete.** An auto-installed skill can cover the same ground as a better
one you already have: a generic `kibana` next to a wrapper bound to your own instance, a
second `gerrit` that talks to a different server, an upstream `skill-creator` next to a
local skill that encodes the conventions that actually apply here. When two descriptions
overlap, the choice becomes a coin flip. Disabling the loser removes the coin flip and
frees the context.

**2. The setting does not survive an update.** The runtime switch is a frontmatter line,
`disable-model-invocation: true`. Write it by hand and the next sync, refresh, or package
update can rewrite or replace the file and drop your edit. When the skill comes from a
registry you cannot patch, there is nothing to fix upstream either.

So the problem is not "turn a skill off once". It is "keep a growing list of them off
while the tools that own them keep changing them".

## What it does

One file declares intent. A background daemon makes the skill files match it.

```json
{
  "patterns": [
    "~/.claude/skills/foo-bar/SKILL.md",
    "~/.agents/skills/foo-bar/SKILL.md"
  ]
}
```

`foo-bar` is a placeholder: replace it with the skills you want to disable. `debounce` and
`rescan_interval` are optional, and take their defaults when the file leaves them out.

For every file `patterns` matches, the daemon computes a one-line difference and writes it:

| Current frontmatter | Path in `patterns` | Path not in `patterns` |
|---|---|---|
| no key | add `disable-model-invocation: true` | no change |
| `false` or another falsy value | replace with `true` | no change |
| `true` | no change | no change |

Nothing else in the file changes. The exact bytes outside that one line stay as they are,
including the end-of-line style, a BOM, and the file mode.

It reacts to a file change within about a second, and sweeps the whole tree every thirty
minutes to repair anything a watcher missed, including a change made while the machine was
asleep.

## Install

```bash
go install github.com/yantonov/skill-model-deactivator/cmd/smd@latest
smd config create     # writes ~/.skill-model-deactivator.json
```

The created file names two example skills, `foo-bar`, because only you can name the ones on
your machine: replace it with the skills you want to disable, then run `smd config show`.

Starting it at login is your own process configuration; the tool writes no unit file,
plist, or scheduled task.

## Use

```bash
smd config create         # write the default config, once
smd config show           # the effective config, both paths
smd config validate       # static checks, no writes
smd serve --once --dry-run  # show the diff, write nothing
smd serve --once            # apply it and exit
smd log                   # last 10 log lines, --lines N for more
smd help                  # paths, commands, options
smd version               # build version, Go version, platform
```

Config is JSON at `~/.skill-model-deactivator.json`, the same path on Windows, Linux, and
macOS, with no flag to move it. A pattern may use either separator, so a copied Windows path
works unchanged. `debounce` and `rescan_interval` are optional: a missing key takes the
default, so the file only has to hold `patterns`.

Log lines go to stdout, and to `smd.log` in the state directory as well, so both work:

```bash
smd serve                 # watch it live
smd serve | grep ERROR    # filter live
smd log --follow          # tail the file instead
```

The file rotates at 1 MiB. stderr carries command failures only, never log lines.

`serve` reads the config once. After an edit, restart it, or use `smd serve --once`.

## Licence

Apache License 2.0, Copyright 2026 Yury Antonov. See [`LICENSE`](LICENSE).

## What it does not do

- It does not edit descriptions, bodies, or any other frontmatter key.
- It does not repair broken frontmatter. The daemon reports a file it cannot parse and
  leaves it alone.
- It does not install, update, or remove skills.
- It touches only the patterns you list. The daemon reads nothing else on disk.

## Design

The daemon is a reconciler: one pure function turns a file plus the config into new bytes
and an action, and a single loop applies it. That gives idempotency, safety against its own
watcher events, and recovery after a crash or a sleep, with no state machine.

Full design, including the measured differences between how Pi and Claude Code parse the
key: [`docs/spec.md`](docs/spec.md).

# skill-model-deactivator

Lets you keep many skills installed but allow only some of them to be invoked
automatically. The rest stay available as explicit commands, and the tool re-applies that
choice every time something rewrites the skill files.

## Why this exists

A skill does two jobs. You can call it yourself, as a slash command. And the model can pick
it on its own, because every installed skill loads its name and description into the
model's context on every turn and competes for the moment when the model decides which
skill to reach for.

Those two jobs do not always belong together. You can want a skill at hand without wanting
the model to reach for it:

- **It is a tool you call deliberately.** A release checklist, a destructive cleanup, a
  long research workflow: useful when you ask for it, a surprise when the model starts it
  because a description happened to match.
- **It overlaps another skill.** A generic skill next to one tailored to your own
  environment, or two skills that cover the same ground in different ways. When two
  descriptions overlap, the choice becomes a coin flip. Keeping only one of them
  model-invocable removes the coin flip.
- **The list is simply too long.** Each description costs context on every turn, and each
  extra candidate makes the model's choice less precise. A short automatic set and a long
  manual one gives you both coverage and focus.

The runtime switch for this is a frontmatter line, `disable-model-invocation: true`. The
skill stays installed and callable by name; the model just stops choosing it. Pi and Claude
Code both honour it.

**The setting does not survive an update.** Skill directories are not yours alone. Agent
installers, plugin registries, and package update commands write into `~/.claude/skills`,
`~/.pi/agent/skills`, and `~/.agents/skills`, and they re-sync those files on the next
update. Write the line by hand and the next sync, refresh, or package update can rewrite or
replace the file and drop your edit. When the skill comes from a registry you cannot patch,
there is nothing to fix upstream either. Auto-installed skills are the sharpest case, but
any skill you do not fully own has the same problem.

So the problem is not "turn a skill off once". It is "decide which skills the model may pick
on its own, and keep that decision in place while the tools that own the files keep
changing them".

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

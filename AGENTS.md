# Notes for coding agents

Read [docs/development/README.md](docs/development/README.md) before changing
anything. It describes the offline check (`./scripts/check.sh`) and
the [project rules](docs/development/README.md#project-rules).

Application packages belong under `internal/`.

Things it doesn't say, because a person wouldn't need telling:

- **Nothing here needs a real telescope.** The tests are offline and use a
  fake share. Don't point the tool at a device, directly or through
  seestar-wormhole, unless the person you're working for asks. Each sync
  pass opens a session on the telescope's share, then logs off and
  disconnects before sleeping.
- **Reviewing a change:** follow [.claude/skills/seestar-copy-review](.claude/skills/seestar-copy-review/SKILL.md).
- **Cutting a release:** follow [.claude/skills/seestar-copy-release](.claude/skills/seestar-copy-release/SKILL.md).

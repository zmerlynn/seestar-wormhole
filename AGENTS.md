# Notes for coding agents

Read [docs/development/README.md](docs/development/README.md) before changing
anything. It has the architecture, the build and offline checks
(`./scripts/check.sh`), and the [project rules](docs/development/README.md#project-rules).

Application packages belong under `internal/`. Keep the existing command and simulator seams internal when changing them.

Two things it doesn't say, because a person wouldn't need telling:

- **Nothing here needs a real telescope or account.** The tests are offline. Don't
  run the proxy against a device, or sign in to the cloud, unless the
  person you're working for asks. It forwards commands to a real telescope and holds its only
  remote session.
- **Reviewing a change:** follow [.claude/skills/wormhole-review](.claude/skills/wormhole-review/SKILL.md).

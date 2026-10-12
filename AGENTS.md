# Publication privacy

Treat this repository, its Git history, documentation images, and release
assets as suitable for eventual public distribution, even while it is private.

Never commit or publish private credentials, cryptographic keys, real device
identities or pairing state, secret invitation or connection addresses, runtime
logs, diagnostic dumps, or personal screenshots and filesystem paths. Use
generic example data for documentation. Public GitHub attribution and required
third-party license credits are acceptable.

Before pushing changes or publishing builds, check the staged content and
artifact contents for sensitive data. Preserve the existing exclusions for
local data, build directories, and the top-level `tests/` directory. Inspect
embedded filenames and metadata when packaging changes could include them.

Keep repository visibility private until the user explicitly requests a change.

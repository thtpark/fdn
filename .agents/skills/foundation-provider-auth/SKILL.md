---
name: foundation-provider-auth
description: "Use when installing/updating Codex or Copilot, checking provider readiness, arranging human-only sign-in, preparing an authorized tmux/VNC assistance session, or diagnosing expired consent without exposing credentials or starting inference."
---

# Provider readiness and protected authentication

Read `agent-cli guide show COMMANDS.md` and `guide show DEPLOYMENT.md`.
Distinguish installation, credentials present, validated authentication, selected
model and authorized inference.

1. Resolve the worker's actual account/HOME/configuration/identity and mode.
   Inspect `provider status --provider NAME`, `preferences show`, `work status`
   and session state. These probes do not authorize paid inference.
   A controller observing explicit login-required/rejected status automatically
   sends a durable protected request to the active Founder and current relevant
   project delegates, before onboarding if needed and without AI inference.
   Read `work status` authentication_requests and the recipient's `message
   inbox`/`message show MESSAGE`; pending delivery is not sent/approved.
   Reuse the existing episode/message operation after uncertainty instead of
   repeating notices. A reply/receipt never proves sign-in; unknown provider
   status does not justify fabricating a login request.
2. Missing provider: obtain approval for the native Node/npm prerequisite and
   exact provider version, then `provider install --provider NAME --version
   EXACT`; explicit `provider update --provider NAME` chooses latest. Keep
   integrity/signature/non-prompting checks; never force an unsupported adapter
   or silently select another provider/model.
   Managed executables are private, with stable shell launchers in `.local/bin`.
   Verify actual login-shell `command -v codex/copilot` and bare `--version`;
   absolute-path success alone does not prove shell usability. Repair an older
   install with `provider link`, then reconnect or add `.local/bin` to the
   current PATH. Do not globally reinstall packages to mask this omission.
3. Login can unlock an ai-mode worker's initial review or automatic work. Resolve
   spending authorization and an approved manual hold before protected sign-in
   where needed; record prior mode for separately authorized restoration.
4. Credentials stay in the dedicated account. Never read/copy another account's
   provider auth, ask for a token/device code/password in chat, capture the UI,
   or publish a private login URL/transcript.
5. A person may explicitly run `provider login --provider NAME` in their own
   visible terminal, without `--json`/`--non-interactive`. Captured/structured
   execution is not a human terminal; do not work around the prerequisite.
6. Remote assistance requires current Founder/project-delegate scope, target
   per-session consent, an explicit requester-open action and an actual 60-900
   second expiry. Use `session request --input FILE` with authentication purpose
   and supported provider. Send a non-secret `message send --input FILE`
   handoff with source/session/project IDs, exact action, UTC deadline/time
   remaining and pending state; protected kind authentication/session must set
   response_requested=false.
7. The target uses `session change SESSION --input FILE` action consent/deny.
   The requester chooses when to use `session open SESSION` in their own human
   terminal. Request, delivery, acknowledgement and ordinary replies are not
   consent. Do not open terminals/screens on the human's behalf.
8. Tmux authentication starts the selected login command. VNC needs an existing,
   authenticated, verified loopback backend and trusted viewer; it does not
   install/unlock a desktop or transfer the requester's application identity.
   Never automate keystrokes through arbitrary consent/login prompts.
9. Expired/revoked sessions need fresh scoped consent; preserve old evidence
   rather than extending expiry or replaying stale actions. Confirm cleanup/
   closure and retain unproven session fences for exact stopped-process recovery.
10. After the human completes login, recheck provider status without inference.
    A finished login command and Codex credentials-present-unverified do not
    prove live vendor usability. Respect task-required provider/model; unknown
    cost/catalog quality is not permission for paid probes or guessed ranking.

Report redacted installed/authentication/model/session states, pending human
action/expiry, and authorized next step. Never claim sign-in, spending consent
or task readiness merely because a provider executable is installed.

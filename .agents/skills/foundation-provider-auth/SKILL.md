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
3. All non-Founder targets are autonomous and unattended. Founder is the only
   human-help coordination point; delegates may be AI agents. They can coordinate
   authorized automated steps but must escalate human-only actions to Founder.
   Login may unlock automatic inference: only Founder may explicitly request a
   pause/resume through `agent mode AGENT --input FILE`, with mode/reason.
   Targets/delegates cannot self-set manual or resume a Founder pause.
4. Credentials stay in the dedicated account. Never read/copy another account's
   provider auth, ask for a token/device code/password in chat, capture the UI,
   or publish a private login URL/transcript.
5. Never wait for a human at the target to run login or record consent. Founder
   can issue `assistance grant --input FILE` once, scoped to the requester/target,
   mode, purpose authentication and optional provider/project. The requester can
   be a delegate agent without inheriting human/credential/task/mode authority.
6. Reuse that persistent revocable grant with `assistance open GRANT --provider
   NAME`. The target prepares the explicitly opened bounded session unattended.
   The grant survives disconnects/restarts until revoked; another live connection
   is not another authorization decision. Current scope/identity/project
   authority and revocation are always rechecked.
7. The protected credential UI must be operated through Founder's human-help
   channel, never exposed to a model, including a delegate AI. Native terminal
   attachment refuses JSON/captured UI. Send non-secret references/actions and
   live-session UTC expiry through messaging; protected kind authentication/
   session has response_requested=false. A message is not a grant or open action.
8. Tmux authentication starts the selected login command. VNC needs an existing,
   authenticated, verified loopback backend and trusted viewer; it does not
   install/unlock a desktop or transfer the requester's application identity.
   Never automate keystrokes through arbitrary consent/login prompts.
9. Expired/closed live sessions can reconnect under the same active grant without
   reapproval. Revoked grants cannot reopen; Founder must explicitly issue a new
   authorization if appropriate. Do not extend a stale session or replay old open
   actions. Confirm cleanup and preserve unproven fences for exact recovery.
10. After the human completes login, recheck provider status without inference.
    A finished login command and Codex credentials-present-unverified do not
    prove live vendor usability. Respect task-required provider/model; unknown
    cost/catalog quality is not permission for paid probes or guessed ranking.

Report redacted installed/authentication/model/session states, pending human
action/expiry, and authorized next step. Never claim sign-in, spending consent
or task readiness merely because a provider executable is installed.

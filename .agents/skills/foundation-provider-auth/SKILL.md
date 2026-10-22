---
name: foundation-provider-auth
description: "Use when installing/updating Codex or Copilot, checking provider readiness, arranging headless/device-code human sign-in, displaying authorized sign-in screens/public verification links, renewing expired sign-in requests through Founder, preparing an authorized tmux/VNC assistance session, or diagnosing expired consent without exposing credentials or starting inference."
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
   Requests expire 15 minutes after creation, including undelivered requests;
   unchanged polling/restart never renews them. Newly requested work, messages,
   mentions, explicit work-run operations and continuation after an observed
   sign-out have their own durable help context and can ask again. Inspect
   requested work before onboarding so an expired idle notice cannot hide it.
   A provider failure gets a fresh read-only authentication check and help
   escalation when explicitly signed out; uncertain process/run fences remain.
   Expired notices leave the pending inbox,
   but `message show MESSAGE` retains their deadline/evidence. Founder can issue
   `agent request-sign-in AGENT --input FILE` with provider, non-secret reason
   and optional matching project_id. Use one new stable operation for this
   intentional refresh and reuse it after uncertainty. The protected urgent
   control also expires after 15 minutes; its handled receipt means the target
   checked, not that authentication succeeded. The target can check while
   paused without resuming inference, sends a fresh superseding notice only
   for explicit login-required/rejected status, and never opens login itself.
   Reuse existing episode/message operations for delivery uncertainty instead
   of repeating notices. Provider device-code and session/grant expiry are
   separate. A reply/receipt never proves sign-in; unknown provider status
   does not justify fabricating a login request.
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
4. Provider credentials stay in the runner's dedicated account. Foundation
   SSH/authentication keys and device/owner certificates stay in the distinct
   protected identity-service account; providers use only its local Unix proxy.
   Provider sign-in is not signed Foundation registration, identity recovery or
   authority to share an SSH account. Never read/copy another account's provider
   auth or identity state, ask for a token/password in chat, or publish a
   credential-bearing URL/transcript. Standard sign-in screens and public
   verification links may be shown to the authorized human through the approved
   terminal/session; that is an expected login step, not itself a privacy
   violation. Do not suppress public generic sign-in links as credentials.
   One-time codes stay in the human login flow; passwords, tokens, cookies,
   credential files/private keys and secret-bearing URLs remain protected.
5. Never wait for a human at the target to run login or record consent. Founder
   can issue `assistance grant --input FILE` once, scoped to the requester/target,
   mode, purpose authentication and optional provider/project. The requester can
   be a delegate agent without inheriting human/credential/task/mode authority.
6. Reuse that persistent revocable grant with `assistance open GRANT --provider
   NAME`. The target prepares the explicitly opened bounded session unattended.
   The grant survives disconnects/restarts until revoked; another live connection
   is not another authorization decision. Current scope/identity/project
   authority and revocation are always rechecked.
7. The human may view the provider's sign-in screen and verification link in
   Founder's authorized human-help channel. Showing them there is permitted;
   public generic URLs and non-secret login instructions may also be relayed in
   messages/model context. Actual credential inputs, one-time codes and private
   sign-in state must not enter a model, including a delegate AI. Native terminal
   attachment still refuses JSON/captured credential UI. Send non-secret references/actions and
   live-session UTC expiry through messaging; protected authentication/permission/
   session kinds have response_requested=false. A message is not a grant or open action.
8. Prefer headless/device-code authentication when the selected CLI supports
   it: `codex login --device-auth`, `copilot login --device-code`. Foundation's
   local provider-login and authentication tmux paths probe that executable's
   `login --help`, use only its advertised device-flow flag and retain native
   login if unsupported; never guess flags or substitute token transfer.
   Copilot retains `--no-auto-update`. A failed capability probe starts no login.
   Device login can require enabling the provider/workspace's device-code
   permission; the human/admin handles that decision, not an automated consent.
   For graphical help, first `screen list`: only actual physical/virtual screens
   with a verified authenticated loopback backend may use VNC; headless targets
   cannot. Use `screen launch SCREEN --application APP` for a configured app on
   that screen, never arbitrary shell/credential injection. Prefer per-screen
   app/capture; if only whole-desktop capture is possible, label it desktop scope
   and require explicit desktop_fallback in the Founder grant/session.
   Ask Founder/current delegates with `assistance request --input FILE`, mode
   vnc, screen_id/application_id, purpose authentication or terminal for general
   human help. This sends a protected notice, not a grant/open/login action.
   Founder grants the exact scope; the human uses `assistance open GRANT --viewer
   browser --listen 127.0.0.1:PORT --assets-dir TRUSTED_NOVNC_DIR` and SSH-forwards
   the same port to localhost. Browser access stays loopback and RFB uses the
   existing SSH/mTLS relay; no public listener/clipboard/UI capture. The helper's
   token-bearing bootstrap URL is private session access, not a public provider
   verification link. VNC never installs/unlocks a desktop or transfers the
   requester's application identity; OS desktop/screen-recording prerequisites
   require their own native approval.
   Never automate keystrokes through arbitrary consent/login prompts.
9. Expired/closed live sessions can reconnect under the same active grant without
   reapproval. Revoked grants cannot reopen; Founder must explicitly issue a new
   authorization if appropriate. Do not extend a stale session or replay old open
   actions. Confirm cleanup and preserve unproven fences for exact recovery.
10. After the human completes login, recheck provider status without inference.
    A finished login command and Codex credentials-present-unverified do not
    prove live vendor usability. Respect task-required provider/model; unknown
    cost/catalog quality is not permission for paid probes or guessed ranking.

Provider references: [Codex headless authentication](https://developers.openai.com/codex/auth/#login-on-headless-devices)
and [Copilot login options](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference#copilot-login-options).
Installed `login --help` remains authoritative for version-specific flags.

Report redacted installed/authentication/model/session states, pending human
action/expiry, and authorized next step. Never claim sign-in, spending consent
or task readiness merely because a provider executable is installed.

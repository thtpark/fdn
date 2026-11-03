---
name: foundation-sharing
description: "Use when Founder views a worker during execution, approves delegate terminal or VNC access for up to 24 hours, selects shared terminals/screens, requests help on a selected resource, reconnects a view, or coordinates a target-acknowledged task-safe input handoff through Foundation."
---

# Unified Foundation terminal and screen sharing

Read `agent-cli guide show COMMANDS.md`, section Unified live sharing.
All viewer bytes use the existing Foundation SSH/mTLS/native relay. Never
connect one agent directly to another agent's SSH, tmux socket, VNC or web port.
Loopback capture backends and requester-local viewers are not peer transports.

## Authority and selection

- Founder opens selected-resource views by current role, without a grant or
  changing automation mode. Viewing does not acquire `work.lock`, create a work
  claim, stop a provider or block another task.
- Only Founder issues a delegate approval. `share approve --input FILE` takes
  exact requester_id/target_id, mode tmux/vnc/both, duration_seconds 1..86400,
  reason, optional project_id and explicit desktop_fallback. The server fixes
  expiry at creation. Reconnects, selection changes, polling and restart never
  renew it. Revoke with `share revoke GRANT --input FILE`; new access after
  expiry needs a fresh Founder decision.
- Delegate approval follows only the target/Founder-selected resources. It
  grants no selection, input, task, project, OS-administrator, retry or spending
  authority. Project-bound approval also rechecks current project authority.
- Inspect `share show AGENT` and `share sessions AGENT`. Only Founder/target may
  list all handoffs; a delegate uses its own `session list/show` records.
  Terminal labels/IDs/generations are metadata, not paths or commands.

## Prepare, select and request help

The worker publishes a managed help shell and its current execution-output
mirror when tmux is available. Additional owner-managed shells use:

```sh
agent-cli share terminal --input FILE
agent-cli share publish
agent-cli share select AGENT_ID --input FILE
agent-cli share help --input FILE
```

Terminal preparation input has name, optional workspace_ref and protected.
Preparation/publication is not command execution or successful remote viewing.
Selection input has mode, resource_id, reason and the current expected_revision.
Only target/self or Founder selects. Selecting `execution` follows subsequent
invocation generations; execution mirrors cannot accept input. Select a managed
shell for interactive help, not an unknown OS process/socket.

Help input has mode, purpose sharing, reason and optional project_id/task_id.
Use the already selected terminal/screen; terminal_id/screen_id and
selection_revision, when supplied, must match. The protected notice reaches
Founder/current project delegates and grants no access. Founder may also
switch directly with `share select`; no person on the unattended target is
required.

VNC needs a published, configured physical/virtual screen and a ready
authenticated loopback backend. A headless browser does not create a desktop.
Whole-desktop fallback must be labelled and included in a delegate's approval.
Observing never launches an application, unlocks a desktop or grants OS capture
permission.

## View through Foundation

```sh
agent-cli share open AGENT_ID --mode tmux
agent-cli share open AGENT_ID --mode vnc --assets-dir TRUSTED_NOVNC_DIR
agent-cli share open AGENT_ID --mode vnc --grant APPROVAL_ID --assets-dir TRUSTED_NOVNC_DIR
agent-cli share status ORIGINAL_OPEN_OPERATION
```

Human viewing requires an explicitly invoked protected terminal/app view.
Keep one-use local browser URLs, pixels and terminal bytes out of ordinary
JSON, drafts, shared records and model context when credential-bearing.
The apps use ephemeral protected terminal/browser surfaces, never a captured
CLI process. Closing a viewer does not stop its producer or the task.

An approved unattended delegate may use `share request AGENT --mode MODE
--grant APPROVAL` to prepare a session and `share stream SESSION` for a native
binary stdin/stdout relay. This is not JSON or a human credential-entry channel.
Protected views are denied to programmatic streams. Use a local ordinary
terminal/RFB client; the Foundation relay remains the only network path.

For unattended VNC, the target operator may set each screen's
vnc_password_file to its existing 0600, same-user plaintext VNC password file
(1..8 raw bytes). The target performs one VNCAuth submission locally and exposes
None security only inside the already Foundation-authenticated channel.
Password/challenge/response/failure text are not delivered to the viewer.
Missing, rejected or unsupported backend authentication is a blocker, not an
automatic password retry. Never put this password/reference in shared notes.

## Input is a separate task-safe lease inside the same viewer

```sh
agent-cli share input SESSION --input FILE
agent-cli share release-input SESSION --input FILE
```

Only Founder requests input. The target's native supervisor waits for a free
existing work slot, reconciled journals/reports and independent fences, then
acknowledges readiness. It never interrupts active execution or changes global
mode. A delegate cannot request/grant its own input. Keys/pointer events are
rejected until acknowledgement; clipboard, power and remote resizing remain
disabled. The target protocol boundary enforces this, not a viewer checkbox.

The control lease holds `work.lock` and a generation-bound cleanup fence.
Ending input joins owned native resources and proves cleanup before retiring
that fence. The logical view and its original expiry remain unchanged; streams
reconnect to the selected resource. An interrupted, uncertain control lease
retains evidence and may require Founder inspection/exact repair. Never clear
its fence, adopt an unknown PID/socket or repeat a command to recover a view.

## Privacy and qualification

Protect a selected terminal/screen before handling credentials with selection
protected:true. Typed authentication screen requests also protect that screen.
Protected UI is human Founder-only, regardless of delegate approval. This is
not automatic image/text redaction: do not display keys, tokens or passwords in
an ordinary shared surface. Previously delivered content cannot be recalled.
The same OS user still owns its files/keys/tools; no secret-isolation or
physical-attestation claim follows.

Native RFB currently supports 3.3/3.7/3.8, None/VNCAuth and bounded
Raw/CopyRect/Cursor/DesktopSize/LastRect updates. Encrypted/unsupported RFB
security or encodings fail closed. Configure/qualify the actual backend rather
than bypass filtering. Founder availability means current authorization,
connectivity and ready native backends, not offline or missing-display access.
Fixtures/builds are not live display, real-provider, privacy or fleet qualification.

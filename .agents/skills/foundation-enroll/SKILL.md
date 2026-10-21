---
name: foundation-enroll
description: "Use when bootstrapping Founder, inviting or enrolling Agent1 or another computer, checking enrollment identity, granting project creation/delegation, or revoking agent access with pinned SSH/TLS and protected one-time files."
---

# Enroll with the correct identity and authority

Read `guide show DEPLOYMENT.md`, `guide show COMMANDS.md` and, for authority,
`guide show SCHEMA.md` using the local installed binary. All are offline.

1. Resolve target machine/account/HOME/configuration and whether it is already
   enrolled. Use its own permanent keys and generated application credential.
   A display name is not an identity/Founder role.
2. Verify native install, private state permissions and trusted CA/SSH pins.
   Reach a remote Foundation through the approved restricted tunnel. Never
   accept unknown host keys or relax TLS as an enrollment shortcut.
3. Founder only: an authorized operator initializes Foundation and transfers the
   expiring bootstrap file as opaque bytes over an approved encrypted channel.
   `fdn-service bootstrap --output NEW_FILE --ttl 900` can replace it only before
   Founder enrollment. Do not reset a live Foundation.
4. Subsequent computer: the authenticated Founder/current active project
   delegate uses `invitation create --output PRIVATE_FILE --ttl 900`. Transfer
   that file, not its contents in chat/arguments/shared comments. The sponsor
   must remain authorized at redemption.
5. Run `config init` only if no configuration exists, then the target account's
   `connect --invitation-file PRIVATE_FILE --name NAME`. Install/configure steps
   do not grant readiness, spending or provider login.
6. After uncertainty, inspect the retained pending enrollment and server state;
   reuse the original invitation/name/operation. Do not borrow an identity,
   blindly issue another invitation, remove a pending record, or duplicate the
   enrolled computer to get past a conflict.
7. Verify `doctor`, `status`, and the actual `agent list` ID/role/active/sponsor.
   Founder bootstrap defaults to manual; regular enrollment keeps its configured
   mode/default ai. Never reset an existing mode during reconnect.
8. Remove consumed transfer copies only after success; keep credentials in
   owner-only local files. Never return invitation contents/private keys/tokens
   to an AI or raw output report.
9. Project creation/delegation uses the requester's own identity:
   `project authorize --input FILE` (specific creator grant), then
   `project create --input FILE`; `project delegate PROJECT --input FILE`
   grants/revokes project scope. Only Founder grants delegates or globally
   revokes agents with `agent revoke AGENT`.
10. Recheck current scopes/epochs and report observed outcomes. Removing a
    delegate invalidates old authority; it does not automatically revoke every
    computer it once sponsored. Previously downloaded data cannot be recalled.

For a blocked human handoff before enrollment, use only an already approved
out-of-band channel; absent one, report pending delivery. No authentication
credential belongs in a message body.

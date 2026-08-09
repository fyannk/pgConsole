---
sidebar_position: 3
title: Access-request review
---

# Access-request review

In a pgToolBox deployment, when an authenticated but unknown identity asks
for access, the proxy creates a `PgToolBoxAccessRequest`. pgConsole's
**dba** review panel is where a reviewer approves or denies those requests.
The console records the decision on the request status only; the operator's
controller materializes the `PgToolBoxUser` after an approval. pgConsole
itself never creates or modifies users, roles, or proxy configuration.

## Enabling

Set `ALLOW_ACCESS_REVIEW=true` and apply `deploy/access-review-role.yaml`.
With the flag off, the panel — reads, writer, and routes — is provably
absent (404). The routes require the `dba` level.

## The panel

`GET /access-requests` shows two lists rendered from the observed
snapshot:

- **Pending requests** — subject, message, and age, each with an
  **Approve** form carrying a level picker, and a **Deny** form.
- **Decided requests** — a read-only audit list showing state, granted
  level, reviewer, and age. Only pending requests accept actions.

The picker offers the closed level set — `view`, `poweruser`, `dba` — the
same ladder the console admits routes by. It is a constant, not a listing:
there is no role object to enumerate, so the options are identical in every
deployment and cannot be emptied by a failed or forbidden read.

## Recording a decision

Approving or denying is a CSRF-guarded, same-origin POST:

- **Approve** must name one of the three grantable levels — a tampered
  form naming anything else is refused with a 400 and no write happens.
  The match is exact: trailing space or different case is off-menu, so a
  value the operator's enum would reject never reaches the API server.
- The write is a merge patch on the request **status subresource** only:
  `state`, `requestedLevel` (approve), `decidedBy` (the reviewer's
  `X-Forwarded-User`), and `decidedAt`.
- It is fire-and-observe: the panel reflects the decision once the informer
  catches up. After approval, the operator's controller creates the user.

Every decision writes one structured **audit** line: the action, the
request, the granted level, the outcome category, and the reviewer identity
labeled proxy-asserted.

## What stays out of scope

pgConsole proves the console's half — reading requests and writing the
decision status. The request-created → approved → user-materialized cycle
across the operator's controller is the pgToolBox integration's joint
concern, not the console's.

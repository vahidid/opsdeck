---
name: report-templates
description: Structure of the reports opsdeck writes with write_report — incident postmortem, audit, and periodic health review — using the stats, timeline, chart and topology blocks. Use before writing any report, so reports of one kind always look the same.
---

# Report templates

Pick the template that fits, keep its sections and their order, and leave out a
section only when there is truly nothing to say in it. Write in the user's
language. Every number comes from something you measured or read; name the
source (a query, a command, an artifact) in the Evidence section.

A workspace can replace these templates: a workspace skill named
`report-templates` is loaded instead of this one.

Rules for every report:

- Open with a `stats` block of three to five figures, then one paragraph that a
  reader who stops there can act on.
- Plot metrics from saved data (`metrics_query` with `save=true`, then a
  `chart` block with `"data"`), not from numbers typed by hand.
- Name hosts, containers and services exactly as the inventory does, in
  backticks, so they link to the resource.
- Close with next steps as a table: action, owner, priority.

## Incident postmortem

Tags: `incident`. Blameless: describe what the system and the process allowed,
not who did it.

1. **Stats** — duration, impact (users, requests or revenue affected), time to
   detect, time to recover. Tone `err` for the damage, `ok` for the current state.
2. **Summary** — what broke, why, and how it was fixed, in three sentences.
3. **Impact** — who and what was affected, and what was not.
4. **Timeline** — a `timeline` block from the first cause to full recovery:
   the triggering change, first alert, detection, each decision, mitigation,
   recovery. Times with their zone.
5. **What happened** — a `topology` block with the failing path, then `chart`
   blocks of the affected metrics with the start and end of the incident as
   `marks`.
6. **Root cause** — the cause, and the contributing factors (why it was not
   caught earlier, why recovery took as long as it did).
7. **What went well / what did not** — two short lists.
8. **Evidence** — commands with their key output, queries, and the artifacts
   saved with `save_artifact`; config changes as `diff` blocks.
9. **Next steps** — the table of actions; then an `actions` block for the ones
   the agent can start now.

## Audit

Tags: `audit`. For a security, configuration, cost or compliance review.

1. **Stats** — items checked, findings by severity (critical, high, medium, low).
2. **Summary** — the overall state and the two or three findings that matter most.
3. **Scope** — what was checked, how, when, and what was left out.
4. **Findings** — one table: severity, resource, finding, recommendation;
   sorted by severity. A `chart` (bar) of findings per host or per category
   when there are many.
5. **Details** — one subsection per critical or high finding: evidence, risk,
   and the fix as a `diff` block or commands.
6. **Evidence** — artifacts and commands.
7. **Next steps** — the table of actions, and an `actions` block.

## Health review

Tags: `health`. For a periodic (weekly, monthly) look at a workspace or a service.

1. **Stats** — availability, error rate, the busiest resource, open alerts;
   `hint` holds the change against the previous period.
2. **Summary** — is it healthy, what changed, what needs attention.
3. **Availability and errors** — `chart` blocks over the whole period, with
   deploys and incidents as `marks`.
4. **Capacity** — a table per host: CPU, memory, disk, with the trend; a
   `[!WARNING]` callout for anything that fills up within the next period.
5. **Changes** — deploys, config changes and incidents of the period (a
   `timeline` block).
6. **Risks** — certificates and domains about to expire, versions out of
   support, backups that did not run.
7. **Next steps** — the table of actions, and an `actions` block.

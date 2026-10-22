# Skills

A skill is a packaged procedure: a Markdown file with a `name` and a `description` in its
front matter, optionally with supporting files. The agent sees every skill's name and
description in its prompt. When a task matches, it loads the full instructions with
`load_skill`, and the references and templates with `load_skill name=… file=…`.

## Where skills come from

- **The workspace**: `skills/<name>.md`, or a folder `skills/<name>/SKILL.md` with
  `references/`, `assets/`, `scripts/` (the Agent Skills layout). Create them in Docs → New →
  Skill, or copy a built-in one.
- **The built-in library**: 20 well-known skills for infrastructure, networking and
  DevOps, shipped inside opsdeck and available in every workspace:

  | Category | Skills |
  |---|---|
  | Kubernetes | `k8s-manifest-generator`, `helm-chart-scaffolding`, `gitops-workflow`, `k8s-security-policies` |
  | Networking | `connectivity-triage`, `hybrid-cloud-networking`, `istio-traffic-management` |
  | Cloud & IaC | `terraform-module-library`, `cost-optimization` |
  | CI/CD | `github-actions-templates`, `gitlab-ci-patterns`, `deployment-pipeline-design`, `secrets-management` |
  | Observability | `prometheus-configuration`, `grafana-dashboards`, `slo-implementation`, `distributed-tracing` |
  | Incidents | `incident-runbook-templates`, `postmortem-writing` |
  | Shell | `bash-defensive-patterns` |

  They come unchanged from [wshobson/agents](https://github.com/wshobson/agents)
  (MIT License, Copyright (c) 2024 Seth Hobson, around 33k stars), and include their
  references, templates and scripts. `internal/skills/library/SOURCES.md` records the
  upstream path and commit of each, and `LICENSE-wshobson-agents` holds the license.
  When the agent loads one, a short note reminds it to use opsdeck's tools (exec on
  hosts, `local_exec` here, `repo_write` for repositories) under the workspace's approval
  rules.

Skills are knowledge the agent follows with judgment. For code that should run exactly
as written (a server bootstrap, an install), use [scripts](scripts.md); a skill can tell
the agent which script to run.

## The Skills page

The page lists the workspace's skills and the library by category. Opening a skill shows
`SKILL.md` and its supporting files. **Customize in this workspace** copies a built-in
skill into `skills/<name>/`, and the copy then replaces it. A switch turns the library off
for the agent in that workspace (`settings.noBuiltinSkills` in `workspace.yaml`).

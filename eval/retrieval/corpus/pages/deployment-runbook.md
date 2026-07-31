---
id: page_deployment_runbook
title: Deployment Runbook
kind: runbook
created: "2026-07-31"
updated: "2026-07-31"
status: active
sensitivity: normal
tags: [deployment, operations]
---
Release the candidate to staging, run the smoke checks, and then promote it to production.
If health checks regress, execute the rollback immediately.

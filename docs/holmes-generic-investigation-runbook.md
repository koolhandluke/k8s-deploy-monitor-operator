# HolmesGPT Generic Investigation Runbook

How HolmesGPT investigates and diagnoses problems. This documents the general approach defined in `holmes/plugins/prompts/generic_ask.jinja2`.

## Overview

HolmesGPT is an **agentic tool-calling LLM**. There is no fixed runbook — it reasons about the problem dynamically and calls tools (kubectl, Prometheus, Grafana, Loki, etc.) iteratively until it reaches a root cause.

The system prompt provides investigation methodology and constraints. Holmes decides which tools to call based on the problem context.

## Investigation Flow

### 1. Skills Check (Optional)

If the skills system is enabled, Holmes first checks a catalog of pre-written troubleshooting guides ("skills"). If a skill matches the issue, Holmes fetches it via `fetch_skill` and follows its steps. Skills take priority over general investigation.

If no skill matches, Holmes skips this step and investigates freely.

### 2. Multi-Phase Investigation

Each investigation follows a structured multi-phase approach:

**Phase 1 — Initial Investigation:**

- Create a task list (via TodoWrite) breaking the problem into smaller steps
- Execute tasks in parallel where possible (e.g., checking pod logs and service config simultaneously)
- Dependent tasks run sequentially (e.g., find pod name, then get pod logs)
- Mark every task in_progress then completed

**Phase N — Follow-Up Phases:**

After completing all tasks in a phase, Holmes self-evaluates:

- "Do I have enough information to answer the question?"
- "Are there gaps or unexplored areas?"
- "Did this phase reveal new questions?"
- "Have I followed the five whys to the actual root cause?"

If any answer is "yes, there are gaps" — a new phase is created targeting those gaps. This repeats until the investigation is complete.

**Example phase progression:**

- Phase 1: Check pod health, metrics, logs, traces
- Phase 2: Traces reveal upstream service is the cause — investigate that service
- Phase 3: Upstream service logs point to a managed storage outage — investigate that
- Final Review: Validate the full chain of events

### 3. Final Review Phase (Mandatory)

Before providing an answer, Holmes must:

- Re-read the original question and confirm the answer addresses it
- Trace every claim back to specific tool output
- Walk through the "five whys" chain and verify each step follows from evidence
- Flag any unsupported statements or assumptions
- Consider alternative explanations not explored
- Use hedging language ("possible", "likely") for unverifiable claims

## Root Cause Methodology

Holmes uses the **"five whys"** approach:

- If service A fails because of service B, investigate service B
- Keep following the chain until reaching the actual root cause
- Don't stop at symptoms

## Kubernetes-Specific Behavior

When investigating Kubernetes problems:

- Walk the ownership chain: deployment -> replicaset -> pod
- Always run `kubectl_describe` + fetch logs on crashed pods
- A "Running" pod with healthy probes can still have application errors — always check logs
- Check ingresses and services for misconfigurations
- Cap investigation at 3 representative pods per deployment
- Search for substring matches if the exact resource name isn't found (handles typos)

## Tool Calling Strategy

- Call multiple tools in parallel when they're independent
- If a tool returns nothing, modify parameters rather than repeating the call
- When searching for resources in a namespace, start with cluster-level tools to locate them first
- Reuse results from previous tool calls — don't re-fetch

## Multi-Cluster Awareness

When Holmes has access to external observability backends (Elasticsearch, Datadog, Loki, etc.):

- These may contain data from clusters other than the local one
- If data is found, verify it belongs to the cluster the user asked about (check cluster/region/environment tags)
- Never silently relabel data from one cluster as another
- If the data is from a different cluster, state this clearly

## Output Style

- Terse and concise — no filler words
- Always include specific resource names, versions, labels
- Distinguish confirmed facts (from tool output) from hypotheses
- If inconclusive, say so — don't guess
- Multiple possible causes listed in a numbered list
- Ignore irrelevant errors that can't be tied to the actual problem

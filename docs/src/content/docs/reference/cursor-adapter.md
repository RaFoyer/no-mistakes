---
title: Cursor Agent Adapter
description: Compatibility, isolation, routing, and benchmark evidence for the native Cursor adapter.
---

The native `cursor` provider is explicit-only and production activation remains an operator decision. It is not selected by `agent: auto`.

## Compatibility contract

- Supported Cursor Agent CLI: `2026.07.17-3e2a980` exactly. Every invocation probes the version and fails closed on drift.
- Authentication: absolute repository-scoped `cursor_config_dir` and `cursor_home_dir` private trees. Version probes, authentication probes, and model calls all replace `HOME` and `CURSOR_CONFIG_DIR`, force `AGENT_CLI_CREDENTIAL_STORE=file`, disable browser opening, and remove ambient Cursor credential variables. A bounded five-second `cursor-agent status --format json` preflight must prove authenticated status plus access and refresh tokens before model startup.
- Transport: prompts, diffs, and schema instructions travel on bounded stdin, never argv. Output uses `--print --output-format stream-json` and requires one initialization event and one successful terminal result.
- Models: routine work pins `cursor-grok-4.5-medium`; deliberate high-risk review confirmation pins `cursor-grok-4.5-high`. Cursor Auto and Fast variants are not production routes.
- Permissions: reviews use read-only ask mode. Mutating duties require Cursor sandboxing, the daemon's isolated-worktree marker, a verified linked Git worktree, and adapter-managed `--force`. Cursor's own worktree feature is disabled.
- Extensions: MCP auto-approval, plugins, extra directories, worktree setup, API keys, headers, and permission/model/session overrides are reserved and cannot be injected through `agent_args_override`.

Cursor has no verified project-instruction suppression mechanism in this supported CLI. In an isolated probe, `AGENTS.md` required the marker `PROJECT_INSTRUCTION_LOADED` while the direct ask-mode prompt required a conflicting marker; Cursor returned `PROJECT_INSTRUCTION_LOADED`. Accordingly, `disable_project_settings: true` refuses Cursor, including as a fallback member. This boundary must remain until a version-pinned suppression mechanism covers `.cursor/rules`, `AGENTS.md`, and `CLAUDE.md` empirically.

## Isolated authentication setup

Create repository-specific `profile` and `home` directories with mode `0700`, then authenticate the supported Cursor binary once under that exact environment. Replace the two example paths with the absolute values configured in `cursor_config_dir` and `cursor_home_dir`:

```sh
(
  umask 077
  env -u CURSOR_API_KEY -u CURSOR_ACCESS_TOKEN -u CURSOR_REFRESH_TOKEN \
    HOME='/Users/you/.config/agent-connectors/owner%2Frepo/cursor/home' \
    CURSOR_CONFIG_DIR='/Users/you/.config/agent-connectors/owner%2Frepo/cursor/profile' \
    AGENT_CLI_CREDENTIAL_STORE=file \
    NO_OPEN_BROWSER=1 \
    /Users/you/.local/bin/cursor-agent login
)
```

The private umask ensures state created during login starts with owner-only permissions. The login is intentionally operator-run and interactive. Daemon launches apply the same private umask, never open a browser, and never fall back to the operator's ambient home, profile, keychain, API key, or tokens. Keep both roots private: all directories must be `0700`; all credential and configuration files must be regular, single-linked, current-user-owned, and `0600`; symlinks and special files are refused. Two exact home-runtime artifacts receive narrower handling. No-mistakes verifies and removes Cursor's current-user-owned Unix socket at `cursor_home_dir/.cursor/private-<1–32 lowercase hex>/worker.sock`. It also removes only the current-user-owned `cursor_home_dir/.local/bin/agent` symlink after verifying its unchanged absolute target is a current-user-owned, single-linked `0700` regular executable at `cursor_home_dir/.local/share/cursor-agent/versions/<YYYY.MM.DD>-<7 lowercase hex>/cursor-agent`; the target is retained. Both removals are checked absent before continuing. The profile tree never permits either exception; alternate link names, symlinked or unexpected targets, and credential/configuration symlinks remain fail-closed. A missing root or unauthenticated status parks the run as authorization-required rather than starting the model or moving to a fallback provider. Existing unsafe entries are never automatically repaired; normalize them explicitly before retrying.

## Model evidence

The account catalog and initialization stream reported these identifier/display-label pairs:

| Requested identifier | Initialization model |
| --- | --- |
| `cursor-grok-4.5-low` | `Cursor Grok 4.5 Low` |
| `cursor-grok-4.5-medium` | `Cursor Grok 4.5 Medium` |
| `cursor-grok-4.5-high` | `Cursor Grok 4.5 High` |
| `cursor-grok-4.5-medium-fast` | `Cursor Grok 4.5 Medium Fast` |

## Benchmark evidence

The same representative Go review fixture contained a direct nil dereference. Each model ran once in an isolated temporary repository on the supported CLI. Every run produced a parseable stream, exact model identity, session ID, successful terminal result, usage object, and recalled the defect.

| Model | Duration | Input | Output | Cache read | Finding recall |
| --- | ---: | ---: | ---: | ---: | --- |
| Low | 7.982 s | 17,052 | 200 | 16,768 | Recalled |
| Medium | 6.532 s | 16,919 | 183 | 16,896 | Recalled |
| High | 6.904 s | 16,922 | 199 | 16,896 | Recalled |
| Medium Fast | 6.018 s | 16,973 | 176 | 16,832 | Recalled |

Medium Fast was about 8% faster than Medium in this single sample, but one fixture is insufficient to establish superior reliability or cost. Medium remains the routine default. High showed no recall advantage on this fixture and remains limited to deliberate confirmation.

A separate fix smoke completed in 5.333 s without an approval prompt, created exactly the requested file in a disposable linked worktree, and returned a successful terminal result. The paired ask-mode run completed without creating the file. Cursor reported token usage but no monetary cost, so cost evidence is limited to the available usage counters.

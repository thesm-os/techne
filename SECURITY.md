<!--
SPDX-License-Identifier: MIT
Copyright Thesmos B.V. 2026
-->

# Security policy

## Supported versions

We support security fixes for the **latest minor release** on `main`.
Once techne reaches v1.0.0, we will publish a formal support window
here.

| Version | Supported |
|---|---|
| latest release | ✅ |
| < latest | ❌ |

## Reporting a vulnerability

**Please do not open a public GitHub issue for security problems.**

Email the maintainers at **[security@thesmos.sh](mailto:security@thesmos.sh)**
or use GitHub's [private vulnerability reporting][github-pvr] feature
on this repository. Include:

- A description of the issue and the impact (RCE, info disclosure,
  supply-chain, etc.).
- Steps to reproduce — a minimal proof-of-concept is ideal.
- The version (`techne --version`), Go toolchain version, and OS where
  you observed the issue.
- Whether you would like credit in the published advisory; we are
  happy to credit reporters who want it and to keep reports
  anonymous if preferred.

We aim to:

- **Acknowledge** receipt within **2 business days**.
- **Triage** and reproduce within **5 business days**.
- **Issue a fix** within **30 days** for high/critical issues, longer
  for lower severities. We will keep you informed if the timeline
  needs to slip.

After a fix ships, we publish a [GitHub Security Advisory][gh-advisories]
on this repository with the CVE (if assigned), affected versions, and
upgrade instructions.

## Scope

Techne runs as a developer tool with full access to the local
filesystem and the agent's project workspace. **The threat model
assumes the operator already trusts the workspace they point techne
at** — techne does not sandbox arbitrary input. The following are
explicitly in scope:

- Memory safety issues, panics on well-formed but adversarial input,
  or unbounded resource consumption that affects an honest operator.
- Supply-chain issues: a transitive dependency with a known CVE that
  affects techne's runtime behaviour.
- Tool behaviour that *escapes the workspace* — for example, an `fs.*`
  tool that follows a symlink outside the operator's intended root
  without consent.
- Privilege escalation paths through the MCP transport.

Out of scope:

- An operator who deliberately hands techne a malicious `compare_to`
  ref, malicious config file, or malicious tool input. Treat techne
  the same way you'd treat `make`, `go test`, or any other
  developer-tool that runs in your user account.
- Bugs in upstream tools (`gopls`, `golangci-lint`, `go test`) that
  techne dispatches to. Report those upstream.

## Hall of fame

We thank the following researchers for responsibly disclosing
security issues in techne:

*None yet.*

[github-pvr]: https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability
[gh-advisories]: https://github.com/thesmos/techne/security/advisories

<!--
SPDX-License-Identifier: MIT
Copyright Thesmos B.V. 2026
-->

## Summary

<!--
One paragraph describing what this PR changes and WHY. Reference the
failure mode or capability gap that motivated the change. If this is
part of a larger initiative, link the parent issue.
-->

## Type of change

<!-- Tick the one that fits. -->

- [ ] Bug fix (non-breaking change that fixes an issue)
- [ ] New tool (adds a new `lang.*` or `fs.*` tool)
- [ ] Enhancement (non-breaking change to an existing tool)
- [ ] Refactor (no observable behaviour change)
- [ ] Documentation
- [ ] Build / CI / tooling
- [ ] Breaking change (API or JSON-schema change to an existing tool)

## How I tested

<!--
Concrete steps a reviewer can repeat. For tool changes, include the
exact `techne` invocation and the expected vs. actual output.
-->

```text
$ make check
... pass

$ techne lang go <tool> ...
{ ... }
```

## Checklist

- [ ] `make check` passes locally
- [ ] New / changed exported symbols have production-grade godoc
- [ ] New tools have tests covering happy path AND build-gate rollback
- [ ] No `//nolint` comments added (if a rule is wrong, update
      `.golangci.yml` instead)
- [ ] Commit messages follow Conventional Commits
- [ ] Linked the relevant issue (`Fixes #N` / `Closes #N` / `Refs #N`)

## Breaking changes

<!--
If this is a breaking change to a tool's JSON schema, the cobra CLI
surface, or the public Go API, describe the migration path here.
Otherwise: "None."
-->

## Related issues / PRs

<!-- Fixes #N, Closes #N, Refs #N, Depends on #N -->

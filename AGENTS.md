# Agent Instructions

## Issues

Before creating an issue, review the available issue templates in the `.github` directory.

When drafting the issue:

- Use the most appropriate template.
- Follow the template structure.
- Fill in all required sections.
- Remove sections that the template explicitly marks as optional or not applicable.
- Do not invent reproduction steps, logs, screenshots, or expected behavior.

## Pull Requests

Before creating a pull request, read `.github/PULL_REQUEST_TEMPLATE.md`.

When drafting the pull request:

- Follow the template structure.
- Use the title format required by the template.
- Fill in or remove each section according to the template guidance.
- Include testing details, or explain why testing was not run.
- Do not invent testing results.
- Do not claim validation, verification, or review steps that were not actually performed.

## Automated Contributions

Fully automated contributions are not considered equivalent to normal community participation.

A contribution may be considered fully automated if it is submitted through an automated agent, or if the submitting account participates in project discussions through an automated agent, without meaningful human review or intervention.

When making this determination, maintainers may consider the overall behavior of the account, including but not limited to disclosed agent usage, interaction patterns, response characteristics, and other available evidence. No single factor is determinative.

Maintainers reserve the right to accept, reject, modify, or reimplement any contribution independently of any action taken against the submitting account. Acceptance of a contribution does not imply acceptance of the submitting account or its contribution method. If an account is determined to be primarily operated through automated processes, we may need to restrict its future participation in contributions until that determination is rescinded.

## Git Commits

When creating commits, follow the repository `git-commit` skill rules:

- Use Conventional Commits title format: `type(scope): subject`.
- Allowed types: `feat`, `fix`, `refactor`, `perf`, `docs`, `style`, `test`, `build`, `ci`, `chore`, `revert`.
- Use a meaningful scope based on the main module, package, or feature.
- Write the subject in imperative mood and describe the actual change.
- Use a concise Markdown list in the commit body, with each item describing one key change.
- Do not invent changes that are not present in the diff.
- Do not describe behavior, refactors, fixes, or tests that are not reflected in the commit.

Include at most one `Co-authored-by` trailer that matches the AI assistant actually used to produce the change.

Examples:

- `Co-authored-by: Codex <267193182+codex@users.noreply.github.com>`
- `Co-authored-by: GitHub Copilot <copilot@github.com>`
- `Co-authored-by: Claude <81847+claude@users.noreply.github.com>`

If you are not one of the listed assistants, do not add a `Co-authored-by` trailer.

Instead, ask the human collaborator to provide the exact `Co-authored-by` trailer to use. Do not invent, infer, or generate one yourself.

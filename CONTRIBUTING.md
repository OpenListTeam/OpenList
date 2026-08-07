# Contributing

## Setup your machine

`OpenList` is written in [Go](https://golang.org/) and [SolidJS](https://www.solidjs.com/).

Prerequisites:

- [git](https://git-scm.com)
- [Go](https://golang.org/doc/install) version declared in [`go.mod`](./go.mod)
- [gcc](https://gcc.gnu.org/)
- [nodejs](https://nodejs.org/)

## Cloning a fork

Fork and clone `OpenList` and `OpenList-Frontend` anywhere:

```shell
git clone https://github.com/<your-username>/OpenList.git
git clone --recurse-submodules https://github.com/<your-username>/OpenList-Frontend.git
```

## Creating a branch

Create a new branch from the `main` branch, with an appropriate name.

```shell
git checkout -b <branch-name>
```

## Preview your change

### backend

```shell
go run main.go
```

### frontend

```shell
pnpm dev
```

## Add a new driver

Copy `drivers/template` folder and rename it, and follow the comments in it.

## Community and policies

By contributing, you agree to follow the repository's code of conduct and license terms.

- Code of conduct: [CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md)
- License: [LICENSE](./LICENSE)
- Security issues: please report privately according to [SECURITY.md](./SECURITY.md)

If your contribution includes substantial AI-assisted content, disclose the tools used and the scope of assistance in the pull request.

## Create a commit

Commit messages should be well formatted, and to make that "standardized".

Submit your pull request. For PR titles, follow [Conventional Commits](https://www.conventionalcommits.org).

<https://github.com/OpenListTeam/OpenList/issues/376>

It's suggested to sign your commits. See: [How to sign commits](https://docs.github.com/en/authentication/managing-commit-signature-verification/signing-commits)

## Submit a pull request

Please make sure your code has been formatted with `go fmt` or [prettier](https://prettier.io/) before submitting.

Push your branch to your `openlist` fork and open a pull request against the `main` branch.

## Merge your pull request

Your pull request will be merged after review. Please wait for the maintainer to merge your pull request after review.

At least 1 approving review is required by reviewers with write access. You can also request a review from maintainers.

## Delete your branch

(Optional) After your pull request is merged, you can delete your branch.

## AI Disclosure

If your pull request includes substantial AI-assisted content, disclose it in the PR description.

The pull request description must follow the repository's pull request template.

Fully automated contributions are not considered equivalent to normal community participation.

A contribution may be considered fully automated if it is submitted through an automated agent, or if the submitting account participates in project discussions through an automated agent, without meaningful human review or intervention.

When making this determination, maintainers may consider the overall behavior of the account, including but not limited to disclosed agent usage, interaction patterns, response characteristics, and other available evidence. No single factor is determinative.

Maintainers reserve the right to accept, reject, modify, or reimplement any contribution independently of any action taken against the submitting account. Acceptance of a contribution does not imply acceptance of the submitting account or its contribution method. If an account is determined to be primarily operated through automated processes, we may need to restrict its future participation in contributions until that determination is rescinded.

Please include:

- Tools used, such as ChatGPT, GitHub Copilot, Claude, Cursor, or other AI tools.
- Usage scope, such as code generation, refactoring, documentation, tests, translation, or review assistance.
- Confirmation that you have reviewed and validated all AI-assisted content before submission.
- Confirmation that the submitted content complies with this repository's license and contribution policies.

Minor AI assistance, such as typo fixes, autocomplete, formatting suggestions, or wording polish, does not need to be disclosed.

---

Thank you for your contribution! Let's make OpenList better together!

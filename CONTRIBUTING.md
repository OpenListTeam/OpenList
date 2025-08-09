# Contributing

## Setup your machine

`OpenList` is written in [Go](https://golang.org/) and [SolidJS](https://www.solidjs.com/).

Prerequisites:

- [git](https://git-scm.com)
- [Go 1.24+](https://golang.org/doc/install)
- [gcc](https://gcc.gnu.org/)
- [nodejs](https://nodejs.org/)

## Cloning a fork

Fork and clone `OpenList` and `OpenList-Frontend` anywhere:

```shell
$ git clone https://github.com/<your-username>/OpenList.git
$ git clone --recurse-submodules https://github.com/<your-username>/OpenList-Frontend.git
```

## Creating a branch

Create a new branch from the `main` branch, with an appropriate name.

```shell
$ git checkout -b <branch-name>
```

## Preview your change

### backend

```shell
$ go run main.go
```

### frontend

```shell
$ pnpm dev
```

## Add a new driver

Copy `drivers/template` folder and rename it, and follow the comments in it.

## Create a commit

Commit messages should be well formatted, and to make that "standardized".

Submit your pull request. For PR titles, follow [Conventional Commits](https://www.conventionalcommits.org).

https://github.com/OpenListTeam/OpenList/issues/376

It's suggested to sign your commits. See: [How to sign commits](https://docs.github.com/en/authentication/managing-commit-signature-verification/signing-commits)

## Submit a pull request

Please make sure your code has been formatted with `go fmt` or [prettier](https://prettier.io/) before submitting.

Push your branch to your `openlist` fork and open a pull request against the `main` branch.

## Merge your pull request

Your pull request will be merged after review. Please wait for the maintainer to merge your pull request after review.

At least 1 approving review is required by reviewers with write access. You can also request a review from maintainers.

## Delete your branch

(Optional) After your pull request is merged, you can delete your branch.

---

Thank you for your contribution! Let's make OpenList better together!

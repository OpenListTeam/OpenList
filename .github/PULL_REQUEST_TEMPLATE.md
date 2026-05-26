<!--
PR title / PR 标题:
- Use Conventional Commits: `type(scope): summary`
- Allowed types: `feat`, `docs`, `fix`, `style`, `refactor`, `chore`
- Scope is required by the current PR title check.
- For breaking changes, add `!`: `feat(driver)!: change auth flow`
-->

## Summary / 摘要

<!--
Briefly describe what changed and why.
简要说明改了什么，以及为什么需要改。
-->

<!--
- List user-visible behavior changes.
- List important implementation changes.
- Mention config, storage, API, or compatibility changes if any.

- 列出用户可感知的行为变化。
- 列出重要实现变化。
- 如涉及配置、存储、API 或兼容性变化，请明确说明。
-->

- [ ] This PR has breaking changes.
      / 此 PR 包含破坏性变更。
- [ ] This PR changes public API, config, storage format, or migration behavior.
      / 此 PR 修改了公开 API、配置、存储格式或迁移行为。
- [ ] This PR requires corresponding changes in related repositories.
      / 此 PR 需要关联仓库同步修改。

Related repository PRs / 关联仓库 PR:

- OpenList-Frontend:
- OpenList-Docs:

## Related Issues / 关联 Issue

<!--
Use `Closes #123`, `Fixes #123`, or `Relates to #123`.
Remove this section if not applicable.
使用 `Closes #123`、`Fixes #123` 或 `Relates to #123`。
不适用时请删除本节。
-->

## Testing / 测试

<!--
Describe commands, platforms, and manual checks.
If not tested, explain why.

说明执行过的命令、测试平台和手动验证。
如果未测试，请说明原因。
-->

- [ ] `go test ./...`
- [ ] Manual test / 手动测试:

## Checklist / 检查清单

- [ ] I have read [CONTRIBUTING](https://github.com/OpenListTeam/OpenList/blob/main/CONTRIBUTING.md).
      / 我已阅读 [CONTRIBUTING](https://github.com/OpenListTeam/OpenList/blob/main/CONTRIBUTING.md)。
- [ ] I confirm this contribution follows the repository license, contribution policy, and code of conduct.
      / 我确认此贡献符合仓库许可证、贡献规范和行为准则。
- [ ] I have formatted the changed code with `gofmt`, `go fmt`, or `prettier` where applicable.
      / 我已按适用情况使用 `gofmt`、`go fmt` 或 `prettier` 格式化变更代码。
- [ ] I have requested review from relevant maintainers or code owners where applicable.
      / 我已在适用情况下请求相关维护者或代码所有者审查。

## AI Disclosure / AI 使用声明

<!--
Please disclose any substantial AI assistance used in this PR.
Minor AI assistance, such as typo fixes, autocomplete, formatting suggestions,
or wording polish, does not need to be disclosed.
Remove this section if not applicable.

请披露此 PR 中使用的重要 AI 辅助内容。
轻微 AI 辅助，例如拼写修正、自动补全、格式建议或文字润色，无需披露。
如不适用，请删除本节。

Deliberate non-disclosure may be treated as a trust and compliance issue.

故意隐瞒 AI 使用情况可能被视为信任与合规问题。
-->

- [ ] This PR includes AI-assisted content.
      / 此 PR 包含 AI 辅助内容。

Tools used / 使用工具:

- [ ] ChatGPT
- [ ] Codex
- [ ] GitHub Copilot
- [ ] Claude
- [ ] Gemini
- [ ] Other (please specify) / 其他（请注明）:

Usage scope / 使用范围:

- [ ] Code generation / 代码生成
- [ ] Refactoring / 重构
- [ ] Documentation / 文档
- [ ] Tests / 测试
- [ ] Translation / 翻译
- [ ] Review assistance / 审查辅助

- [ ] I have reviewed and validated all AI-assisted content included in this PR.
      / 我已审核并验证此 PR 中的所有 AI 辅助内容。
- [ ] I have ensured that all AI-assisted commits include `Co-Authored-By` attribution.
      / 我已确保所有 AI 辅助提交都包含 `Co-Authored-By` 归属信息。
- [ ] I can reproduce all AI-assisted content included in this PR without any AI tools.
      / 我可以在没有任何 AI 工具的情况下重现此 PR 中包含的所有 AI 辅助内容。

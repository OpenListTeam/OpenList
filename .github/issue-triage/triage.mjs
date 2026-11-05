import { readFile } from 'node:fs/promises'

export default async function triage() {
  const event = JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH, 'utf8'))
  if (event.action !== 'opened' || !event.issue || event.issue.pull_request ||
      event.sender.type === 'Bot' || event.issue.title.startsWith('[Announcements]')) return

  const { GITHUB_TOKEN, GITHUB_REPOSITORY, GITHUB_API_URL,
    CUSTOM_API_BASE_URL, CUSTOM_API_KEY, CUSTOM_API_MODEL } = process.env
  if (!GITHUB_TOKEN || !GITHUB_REPOSITORY || !GITHUB_API_URL) {
    throw new Error('Missing GitHub configuration')
  }

  async function github(path, method = 'GET', body) {
    const response = await fetch(`${GITHUB_API_URL}/repos/${GITHUB_REPOSITORY}/${path}`, {
      method,
      headers: {
        Authorization: `Bearer ${GITHUB_TOKEN}`,
        Accept: 'application/vnd.github+json',
        'Content-Type': 'application/json',
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(30000),
    })
    if (!response.ok) throw new Error(`GitHub request failed: ${response.status}`)
    return response.json()
  }

  const issuePath = `issues/${event.issue.number}`
  const issue = await github(issuePath)
  if (issue.state !== 'open' || issue.locked) return
  if (!CUSTOM_API_BASE_URL || !CUSTOM_API_KEY || !CUSTOM_API_MODEL) {
    throw new Error('Missing CUSTOM_API_BASE_URL, CUSTOM_API_KEY or CUSTOM_API_MODEL')
  }

  const prompt = await readFile(new URL('./system-prompt.md', import.meta.url), 'utf8')
  const response = await fetch(`${CUSTOM_API_BASE_URL.replace(/\/$/, '')}/chat/completions`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${CUSTOM_API_KEY}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      model: CUSTOM_API_MODEL,
      messages: [
        { role: 'system', content: prompt },
        { role: 'user', content: JSON.stringify({
          title: issue.title,
          body: issue.body,
          labels: issue.labels.map(label => label.name),
        }) },
      ],
      temperature: 0.2,
    }),
    signal: AbortSignal.timeout(120000),
  })
  if (!response.ok) throw new Error(`LLM request failed: ${response.status}`)
  const result = await response.json()
  const decision = JSON.parse(result.choices[0].message.content)
  const questions = {
    version_not_specific: '请提供完整的 OpenList 版本号，避免仅填写“最新版”或“latest”',
    driver_unclear: '请说明问题涉及的存储驱动，以及出问题的操作是否通过该驱动进行',
    reproduction_unclear: '请按顺序描述触发问题的操作，并说明问题是否每次都会发生',
    expected_actual_unclear: '请说明具体操作的预期结果和实际结果，以及在哪一步出现差异',
    logs_not_relevant: '请提供执行出错操作时对应的错误日志及必要上下文，提交前请移除密码、Token、私钥等敏感信息',
    configuration_check_needed: '请核对相关存储和功能配置，并说明已检查的配置项及结果，请勿提交密码、Token、私钥等敏感信息',
    network_check_needed: '请确认运行 OpenList 的环境能否直接访问对应存储服务，并补充相关网络错误及检查结果',
    credentials_check_needed: '请通过存储服务官方渠道确认账号状态和授权是否有效，仅反馈检查结果，请勿提交密码、Token 或其他凭据',
    upstream_check_needed: '请确认同一操作在对应存储服务的官方网页或客户端中是否正常，并补充检查结果',
    feature_context_unclear: '请说明功能的使用场景、期望行为，以及现有方式有哪些限制',
  }
  if (!decision || Array.isArray(decision) || Object.keys(decision).length !== 1 ||
      !Array.isArray(decision.findings) || decision.findings.length > 3 ||
      !decision.findings.every(finding => typeof finding === 'string' && Object.hasOwn(questions, finding))) {
    throw new Error('Invalid triage decision')
  }

  if (decision.findings.length) {
    const body = '这是自动内容初审，供维护者进一步排查参考，并非最终处理结论。请补充以下信息：\n\n' +
      decision.findings.map(finding => `- ${questions[finding]}`).join('\n') +
      '\n\n请直接在当前 Issue 补充，后续由维护者查看。'
    await github(`${issuePath}/comments`, 'POST', { body })
  }
}

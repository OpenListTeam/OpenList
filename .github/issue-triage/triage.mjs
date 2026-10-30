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
    version: '请提供具体的 OpenList 版本号',
    driver: '请提供使用的存储驱动名称',
    reproduction: '请提供能够复现问题的操作步骤',
    logs: '请提供相关错误日志，提交前请移除密码、Token、私钥等敏感信息',
    expected_behavior: '请说明期望的功能行为和使用场景',
  }
  const allowedMissing = {
    bug: ['version', 'driver', 'reproduction', 'logs'],
    enhancement: ['expected_behavior'],
    none: [],
  }
  if (!decision || Array.isArray(decision) || Object.keys(decision).length !== 2 ||
      !['bug', 'enhancement', 'none'].includes(decision.category) ||
      !Array.isArray(decision.missing) ||
      decision.missing.length > allowedMissing[decision.category].length ||
      !decision.missing.every(field => allowedMissing[decision.category].includes(field))) {
    throw new Error('Invalid triage decision')
  }

  if (decision.category !== 'none') {
    await github(`${issuePath}/labels`, 'POST', { labels: [decision.category] })
  }
  if (decision.missing.length) {
    const body = '为便于维护者进一步了解此 Issue，请补充以下信息：\n\n' +
      decision.missing.map(field => `- ${questions[field]}`).join('\n')
    await github(`${issuePath}/comments`, 'POST', { body })
  }
}

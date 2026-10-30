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
  const allowedLabels = ['bug', 'enhancement', 'invalid']
  if (!decision || !Array.isArray(decision.labels) ||
      !decision.labels.every(label => allowedLabels.includes(label)) ||
      typeof decision.comment !== 'string') {
    throw new Error('Invalid triage decision')
  }

  if (decision.labels.length) await github(`${issuePath}/labels`, 'POST', { labels: decision.labels })
  if (decision.comment.trim()) {
    await github(`${issuePath}/comments`, 'POST', { body: decision.comment.trim() })
  }
}

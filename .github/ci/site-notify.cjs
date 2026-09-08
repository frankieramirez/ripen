'use strict'

const sourceRepo = 'frankieramirez/ripen'
const siteRepo = 'frankieramirez/ripen-site'

function parts(name) {
  const [owner, repo] = name.split('/')
  return { owner, repo }
}

function runOf(context) {
  return context.payload.workflow_run || {}
}

async function status(github, pr, state, description) {
  const { owner, repo } = parts(sourceRepo)
  await github.rest.repos.createCommitStatus({ owner, repo, sha: pr.head.sha, state, context: 'site/docs', description })
}

async function dispatch(token, workflow, inputs) {
  if (!token) throw new Error('SITE_WORKFLOW_TOKEN is not configured')
  const response = await fetch(`https://api.github.com/repos/${siteRepo}/actions/workflows/${workflow}/dispatches`, {
    method: 'POST',
    headers: { accept: 'application/vnd.github+json', authorization: `Bearer ${token}`, 'content-type': 'application/json', 'x-github-api-version': '2022-11-28' },
    body: JSON.stringify({ ref: 'main', inputs })
  })
  if (!response.ok) throw new Error(`site dispatch failed (${response.status})`)
}

async function changed(github, base, head) {
  const { owner, repo } = parts(sourceRepo)
  const result = await github.rest.repos.compareCommitsWithBasehead({ owner, repo, basehead: `${base}...${head}`, per_page: 100 })
  const files = result.data.files
  if (!Array.isArray(files) || files.length >= 300 || result.data.truncated === true) return true
  return files.some(file => [file.filename, file.previous_filename].some(path => path === 'CONTEXT.md' || path?.startsWith('docs/')))
}

async function findPR(github, run) {
  const { owner, repo } = parts(sourceRepo)
  const listed = Array.isArray(run.pull_requests) ? run.pull_requests : []
  const candidates = []
  for (const item of listed) {
    try { candidates.push((await github.rest.pulls.get({ owner, repo, pull_number: item.number })).data) } catch {}
  }
  if (!candidates.length && run.head_branch && run.head_repository?.full_name) {
    const head = `${run.head_repository.full_name.split('/')[0]}:${run.head_branch}`
    const result = await github.rest.pulls.list({ owner, repo, state: 'open', head, per_page: 100 })
    if (!Array.isArray(result.data) || result.data.length >= 100) throw new Error('pull request listing was truncated')
    candidates.push(...result.data)
  }
  return candidates.find(pr => pr.state === 'open' && pr.head?.sha && (pr.head.sha === run.head_sha || pr.merge_commit_sha === run.head_sha)) || null
}

async function notifyPR(github, core, token, run) {
  const pr = await findPR(github, run)
  if (!pr) return
  const docs = await changed(github, pr.base.sha, pr.head.sha)
  if (!docs) return
  await status(github, pr, 'pending', 'site validation queued')
  try {
    await dispatch(token, 'validate.yaml', { pr_number: String(pr.number), head_sha: pr.head.sha })
  } catch (error) {
    await status(github, pr, 'error', 'site validation could not be queued')
    core.setFailed(error.message)
  }
}

async function notifyMain(core, token, context) {
  if (context.eventName !== 'push' || context.ref !== 'refs/heads/main') return
  try {
    await dispatch(token, 'deploy.yaml', {})
  } catch (error) { core.setFailed(error.message) }
}

module.exports = async function siteNotify(github, context, core, dispatchToken) {
  const run = runOf(context)
  if (context.payload.repository?.full_name !== sourceRepo) return
  if (run.event === 'pull_request') return notifyPR(github, core, dispatchToken, run)
  if (run.event === 'push') return
  return notifyMain(core, dispatchToken, context)
}

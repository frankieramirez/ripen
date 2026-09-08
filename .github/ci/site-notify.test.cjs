'use strict'

const assert = require('node:assert/strict')
const { test } = require('node:test')
const notify = require('./site-notify.cjs')

function fake(run, calls = []) {
  return {
    rest: {
      repos: { compareCommitsWithBasehead: async () => ({ data: { files: [{ filename: 'docs/a.md' }] } }), createCommitStatus: async value => calls.push(['status', value]) },
      pulls: { get: async () => ({ data: { number: 4, state: 'open', base: { sha: 'b'.repeat(40) }, head: { sha: 'c'.repeat(40) }, merge_commit_sha: run.head_sha } }), list: async () => ({ data: [] }) }
    }
  }
}

const context = run => ({ payload: { repository: { full_name: 'frankieramirez/ripen' }, workflow_run: run } })
const pushContext = (branch, sha) => ({ eventName: 'push', ref: `refs/heads/${branch}`, sha, payload: { repository: { full_name: 'frankieramirez/ripen' }, ref: `refs/heads/${branch}` } })
const core = () => ({ failures: [], setFailed(message) { this.failures.push(message) } })

test('fork guard skips all work', async () => {
  const github = { rest: new Proxy({}, { get() { throw new Error('GitHub API should not be called') } }) }
  const original = global.fetch
  global.fetch = async () => { throw new Error('fetch should not be called') }
  try {
    await notify(github, { ...pushContext('main', 'a'.repeat(40)), payload: { repository: { full_name: 'someone/ripen' } } }, core(), 'token')
  } finally { global.fetch = original }
})

test('successful main docs change dispatches deploy', async () => {
  const calls = []
  const original = global.fetch
  global.fetch = async (_url, options) => { calls.push(JSON.parse(options.body)); return { ok: true } }
  try { await notify(fake({}), pushContext('main', 'd'.repeat(40)), core(), 'token') } finally { global.fetch = original }
  assert.deepEqual(calls, [{ ref: 'main', inputs: {} }])
})

test('other branch does not dispatch', async () => {
  const calls = []
  const original = global.fetch
  global.fetch = async () => { calls.push(true); return { ok: true } }
  try { await notify(fake({}), pushContext('release', 'd'.repeat(40)), core(), 'token') } finally { global.fetch = original }
  assert.equal(calls.length, 0)
})

test('workflow run for a push is ignored', async () => {
  let called = false
  const original = global.fetch
  global.fetch = async () => { called = true; return { ok: true } }
  try { await notify(fake({}), context({ event: 'push', conclusion: 'success', head_branch: 'main', head_sha: 'd'.repeat(40) }), core(), 'token') } finally { global.fetch = original }
  assert.equal(called, false)
})

test('renamed documentation dispatches validation', async () => {
  const g = fake({ head_sha: 'e'.repeat(40) })
  g.rest.repos.compareCommitsWithBasehead = async () => ({ data: { files: [{ filename: 'README.md', previous_filename: 'docs/old.md' }] } })
  let dispatched = false
  const original = global.fetch
  global.fetch = async () => { dispatched = true; return { ok: true } }
  try { await notify(g, context({ event: 'pull_request', head_sha: 'e'.repeat(40), pull_requests: [{ number: 4 }] }), core(), 'token') } finally { global.fetch = original }
  assert.equal(dispatched, true)
})

test('pull request merge sha association dispatches validation', async () => {
  const calls = []
  const original = global.fetch
  global.fetch = async (_url, options) => { calls.push(JSON.parse(options.body)); return { ok: true } }
  try { await notify(fake({ head_sha: 'e'.repeat(40) }), context({ event: 'pull_request', head_sha: 'e'.repeat(40), head_branch: 'feature', head_repository: { full_name: 'fork/ripen' }, pull_requests: [{ number: 4 }] }), core(), 'token') } finally { global.fetch = original }
  assert.equal(calls[0].inputs.pr_number, '4')
})

test('stale pull request head is ignored', async () => {
  const github = fake({ head_sha: 'f'.repeat(40) })
  github.rest.pulls.get = async () => ({ data: { number: 4, state: 'open', head: { sha: '1'.repeat(40) }, base: { sha: '2'.repeat(40) }, merge_commit_sha: '3'.repeat(40) } })
  let called = false
  const original = global.fetch
  global.fetch = async () => { called = true; return { ok: true } }
  try { await notify(github, context({ event: 'pull_request', head_sha: 'f'.repeat(40), pull_requests: [{ number: 4 }] }), core(), 'token') } finally { global.fetch = original }
  assert.equal(called, false)
})

test('expired dispatch token records pending and error statuses', async () => {
  const calls = []
  const c = core()
  const original = global.fetch
  global.fetch = async () => ({ ok: false, status: 401 })
  try { await notify({ ...fake({ head_sha: 'g'.repeat(40) }), rest: { ...fake({ head_sha: 'g'.repeat(40) }).rest, repos: { ...fake({ head_sha: 'g'.repeat(40) }).rest.repos, createCommitStatus: async value => calls.push(value) } } }, context({ event: 'pull_request', head_sha: 'g'.repeat(40), pull_requests: [{ number: 4 }] }), c, 'token') } finally { global.fetch = original }
  assert.deepEqual(calls.map(v => v.state), ['pending', 'error'])
  assert.equal(c.failures.length, 1)
  assert.equal(c.failures[0], 'site dispatch failed (401)')
})

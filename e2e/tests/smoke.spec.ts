import { expect, test, type APIRequestContext } from '@playwright/test'

// Smoke tests for the built image against the fixtures in compose.yaml: two
// cameras on 2026-01-15 (UTC), one clip each. Front Door has a camera snapshot;
// Driveway is a fragmented MP4 with no snapshot (remux + ffmpeg frame grab).

const password = process.env.NVR_PASSWORD ?? ''
const dayFrom = Date.UTC(2026, 0, 15)
const dayTo = dayFrom + 24 * 3600 * 1000

type Clip = { id: number; camera_id: number; start: number; end: number; codec: string; width: number; height: number; thumb: boolean; thumb_kind?: string; file: string }
type Camera = { id: number; name: string }

test.describe.configure({ mode: 'serial' })

async function signIn(request: APIRequestContext) {
  const res = await request.post('/api/login', { data: { username: 'admin', password } })
  expect(res.status(), await res.text()).toBe(200)
}

test('health and version are served without signing in', async ({ request }) => {
  const health = await request.get('/healthz')
  expect(health.ok()).toBeTruthy()
  const version = await request.get('/api/version')
  expect(version.ok()).toBeTruthy()
  expect(await request.get('/api/clips?from=0&to=1').then((r) => r.status())).toBe(401)
})

test('indexes the WebDAV recordings', async ({ request }) => {
  expect(password, 'NVR_PASSWORD from the server log').not.toBe('')
  await signIn(request)
  await expect
    .poll(
      async () => {
        const s = await (await request.get('/api/status')).json()
        return { ready: s.index.clips.ready ?? 0, thumbs: s.index.thumbs.done ?? 0, cameras: s.index.cameras, paired: s.index.paired_snapshots }
      },
      { timeout: 60_000, intervals: [500, 1000, 2000] },
    )
    .toEqual({ ready: 2, thumbs: 2, cameras: 2, paired: 1 })
})

test('serves clips, thumbnails and playable streams', async ({ request }) => {
  await signIn(request)
  const cameras: Camera[] = await (await request.get('/api/cameras')).json()
  expect(cameras.map((c) => c.name).sort()).toEqual(['Driveway', 'Front Door'])
  const camName = new Map(cameras.map((c) => [c.id, c.name]))

  const { clips }: { clips: Clip[] } = await (await request.get(`/api/clips?from=${dayFrom}&to=${dayTo}`)).json()
  expect(clips).toHaveLength(2)

  for (const c of clips) {
    const name = camName.get(c.camera_id)
    expect.soft(c.codec, name).toBe('H.264')
    expect.soft([c.width, c.height], name).toEqual([640, 360])
    expect.soft(c.end - c.start, name).toBeGreaterThan(5000)
    expect.soft(c.thumb_kind, name).toBe(name === 'Front Door' ? 'snapshot' : 'frame')

    const thumb = await request.get(`/api/clips/${c.id}/thumb`)
    expect(thumb.status(), `${name} thumb`).toBe(200)
    expect(thumb.headers()['content-type']).toMatch(/^image\//)
    expect((await thumb.body()).length).toBeGreaterThan(500)

    const stream = await request.get(`/api/clips/${c.id}/stream`, { headers: { Range: 'bytes=0-65535' } })
    expect(stream.status(), `${name} stream`).toBe(206)
    expect(stream.headers()['content-type']).toBe('video/mp4')
    const head = (await stream.body()).toString('latin1')
    expect(head.slice(4, 8), `${name} starts with ftyp`).toBe('ftyp')
    if (name === 'Driveway') {
      // Fragmented uploads are remuxed with the index up front so browsers can seek.
      expect(head.indexOf('moov'), 'remuxed moov in the first 64 KB').toBeGreaterThan(0)
      expect(head.indexOf('moof'), 'no fragments in the remuxed header').toBe(-1)
    }
  }
})

test('signs in and plays the newest clip in the browser', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`))
  page.on('console', (m) => m.type() === 'error' && errors.push(`console: ${m.text()}`))

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  await page.locator('#signin-password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()

  const cams = page.getByRole('group', { name: 'Camera' })
  await expect(cams.getByRole('button', { name: 'Front Door' })).toBeVisible()
  await expect(cams.getByRole('button', { name: 'Driveway' })).toBeVisible()

  // Opens on the newest clip (Driveway 10:00:30); its thumbnail is a decoded image.
  const clipList = page.getByRole('complementary', { name: 'Clips' })
  const clip = clipList.getByRole('button', { name: /^Clip at 10:00:30/ })
  await expect(clip).toBeVisible()
  const thumb = clip.locator('img')
  await expect.poll(() => thumb.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth)).toBeGreaterThan(0)

  // The visible player has the stream loaded far enough to know its duration.
  const video = page.locator('main[aria-label="Video"] video:not([aria-hidden="true"])')
  await expect.poll(() => video.evaluate((v: HTMLVideoElement) => v.currentSrc)).toContain('/api/clips/')
  await expect.poll(() => video.evaluate((v: HTMLVideoElement) => v.readyState), { timeout: 15_000 }).toBeGreaterThanOrEqual(1)

  // Switching camera loads that camera's clip.
  await cams.getByRole('button', { name: 'Front Door' }).click()
  await expect(clipList.getByRole('button', { name: /^Clip at 10:00:00/ })).toBeVisible()

  expect(errors).toEqual([])
})

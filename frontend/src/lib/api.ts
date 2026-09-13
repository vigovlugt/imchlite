export interface Asset {
  id: number
  checksum: string
  mimeType?: string
  type: 'image' | 'video'
  localDateTime?: number
  timeZone?: string
  latitude?: number
  longitude?: number
  city?: string
  country?: string
  width?: number
  height?: number
  durationMs?: number
  orientation?: number
  isFavorite: boolean
}

export interface AssetPage {
  assets: Asset[]
  nextCursor?: string
}

export interface Facets {
  countries: string[]
  cities: string[]
  paths: string[]
  minTime?: number
  maxTime?: number
  totalCount: number
}

export interface AssetFilters {
  type?: 'image' | 'video'
  city?: string
  country?: string
  includePaths: string[]
  excludePaths: string[]
  from?: number
  until?: number
}

export function thumbUrl(checksum: string): string {
  return `/api/thumb/${checksum}`
}

export function mediaUrl(checksum: string): string {
  return `/api/media/${checksum}`
}

async function getJson<T>(url: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(url, { signal })
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) message = body.error
    } catch {
      // ignore body parse failures
    }
    throw new Error(message)
  }
  return (await res.json()) as T
}

export type IndexPhase = 'indexing' | 'processing' | 'completed' | 'failed'

export interface IndexStatus {
  startedAt: string
  discovered: number
  processed: number
  errored: number
  phase: IndexPhase
  etaSeconds?: number
  completed: boolean
  failed: boolean
  error?: string
}

export function fetchFacets(signal?: AbortSignal): Promise<Facets> {
  return getJson<Facets>('/api/facets', signal)
}

export function fetchIndexStatus(signal?: AbortSignal): Promise<IndexStatus> {
  return getJson<IndexStatus>('/api/index/status', signal)
}

function filtersToParams(f: AssetFilters, cursor?: string): string {
  const params = new URLSearchParams()
  if (f.type) params.set('type', f.type)
  if (f.city) params.set('city', f.city)
  if (f.country) params.set('country', f.country)
  for (const p of f.includePaths) params.append('include_path', p)
  for (const p of f.excludePaths) params.append('exclude_path', p)
  if (f.from !== undefined) params.set('from', String(f.from))
  if (f.until !== undefined) params.set('until', String(f.until))
  params.set('limit', '200')
  if (cursor) params.set('cursor', cursor)
  return params.toString()
}

export function fetchAssets(
  filters: AssetFilters,
  cursor?: string,
  signal?: AbortSignal,
): Promise<AssetPage> {
  return getJson<AssetPage>(`/api/assets?${filtersToParams(filters, cursor)}`, signal)
}

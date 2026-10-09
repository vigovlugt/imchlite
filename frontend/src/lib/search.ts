import { useInfiniteQuery } from "@tanstack/react-query";
import { fetchAssets, type AssetFilters } from "#/lib/api";

/** The url search params of the timeline and the asset viewer on top of it. */
export interface AssetSearch {
  context_query?: string;
  /** checksum of the asset to find similar assets to */
  similar_to?: string;
  type?: "image" | "video";
  city?: string;
  country?: string;
  /** text read from the asset by ocr */
  text?: string;
  include?: string;
  exclude?: string;
  from?: number;
  until?: number;
}

export function parseSearch(search: Record<string, unknown>): AssetSearch {
  const s: AssetSearch = {};
  if (typeof search.context_query === "string" && search.context_query)
    s.context_query = search.context_query;
  // A similar-to search replaces a text search; never send both.
  else if (
    typeof search.similar_to === "string" &&
    /^[0-9a-f]{64}$/.test(search.similar_to)
  )
    s.similar_to = search.similar_to;
  if (search.type === "image" || search.type === "video") s.type = search.type;
  if (typeof search.city === "string" && search.city) s.city = search.city;
  if (typeof search.country === "string" && search.country)
    s.country = search.country;
  if (typeof search.text === "string" && search.text) s.text = search.text;
  if (typeof search.include === "string" && search.include)
    s.include = search.include;
  if (typeof search.exclude === "string" && search.exclude)
    s.exclude = search.exclude;
  if (typeof search.from === "number" && Number.isFinite(search.from))
    s.from = search.from;
  if (typeof search.until === "number" && Number.isFinite(search.until))
    s.until = search.until;
  return s;
}

export function filtersFromSearch(search: AssetSearch): AssetFilters {
  return {
    type: search.type,
    city: search.city,
    country: search.country,
    ocrText: search.text,
    includePaths: search.include ? search.include.split(",") : [],
    excludePaths: search.exclude ? search.exclude.split(",") : [],
    from: search.from,
    until: search.until,
    contextQuery: search.context_query,
    similarTo: search.similar_to,
  };
}

/**
 * The paged assets matching the filters. The timeline and the asset viewer
 * share the query, so the viewer steps through the pages the timeline loaded.
 */
export function useAssetsQuery(filters: AssetFilters) {
  return useInfiniteQuery({
    queryKey: ["assets", filters],
    queryFn: ({ pageParam, signal }) => fetchAssets(filters, pageParam, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
}

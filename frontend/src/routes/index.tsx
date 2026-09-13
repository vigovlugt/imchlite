import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  keepPreviousData,
  useInfiniteQuery,
  useQuery,
} from "@tanstack/react-query";
import {
  fetchAssets,
  fetchFacets,
  mediaUrl,
  thumbUrl,
  type Asset,
  type AssetFilters,
} from "#/lib/api";

interface AssetSearch {
  type?: "image" | "video";
  city?: string;
  country?: string;
  include?: string;
  exclude?: string;
  from?: number;
  until?: number;
}

function parseSearch(search: Record<string, unknown>): AssetSearch {
  const s: AssetSearch = {};
  if (search.type === "image" || search.type === "video") s.type = search.type;
  if (typeof search.city === "string" && search.city) s.city = search.city;
  if (typeof search.country === "string" && search.country)
    s.country = search.country;
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

export const Route = createFileRoute("/")({
  validateSearch: parseSearch,
  component: Home,
});

function filtersFromSearch(search: AssetSearch): AssetFilters {
  return {
    type: search.type,
    city: search.city,
    country: search.country,
    includePaths: search.include ? search.include.split(",") : [],
    excludePaths: search.exclude ? search.exclude.split(",") : [],
    from: search.from,
    until: search.until,
  };
}

const dayFormat = new Intl.DateTimeFormat(undefined, {
  weekday: "long",
  year: "numeric",
  month: "long",
  day: "numeric",
});

interface DayGroup {
  key: string;
  label: string;
  assets: Asset[];
}

function groupByDay(assets: Asset[]): DayGroup[] {
  const groups: DayGroup[] = [];
  let current: DayGroup | undefined;
  for (const a of assets) {
    const key = a.localDateTime
      ? toDateKey(new Date(a.localDateTime * 1000))
      : "unknown";
    if (!current || current.key !== key) {
      current = {
        key,
        label:
          key === "unknown"
            ? "Unknown date"
            : dayFormat.format(new Date(a.localDateTime! * 1000)),
        assets: [],
      };
      groups.push(current);
    }
    current.assets.push(a);
  }
  return groups;
}

function toDateKey(d: Date): string {
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${d.getFullYear()}-${m}-${day}`;
}

function dateInputToEpoch(value: string, endOfDay = false): number | undefined {
  const [y, m, d] = value.split("-").map(Number);
  if (!y || !m || !d) return undefined;
  const date = endOfDay
    ? new Date(y, m - 1, d, 23, 59, 59, 999)
    : new Date(y, m - 1, d);
  return Math.floor(date.getTime() / 1000);
}

function epochToDateInput(sec: number | undefined): string {
  if (sec === undefined) return "";
  return toDateKey(new Date(sec * 1000));
}

function formatDuration(ms: number | undefined): string {
  if (!ms) return "";
  const total = Math.round(ms / 1000);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

function Home() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const filters = useMemo(() => filtersFromSearch(search), [search]);

  const setFilters = useCallback(
    (patch: Partial<AssetSearch>) => {
      void navigate({ search: (prev: AssetSearch) => ({ ...prev, ...patch }) });
    },
    [navigate],
  );

  const facetsQuery = useQuery({
    queryKey: ["facets"],
    queryFn: ({ signal }) => fetchFacets(signal),
  });

  const assetsQuery = useInfiniteQuery({
    queryKey: ["assets", filters],
    queryFn: ({ pageParam, signal }) => fetchAssets(filters, pageParam, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
    placeholderData: keepPreviousData,
  });

  const assets = useMemo(
    () => assetsQuery.data?.pages.flatMap((p) => p.assets) ?? [],
    [assetsQuery.data],
  );
  const days = useMemo(() => groupByDay(assets), [assets]);

  const sentinelRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = sentinelRef.current;
    if (!el) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (
          entries[0].isIntersecting &&
          assetsQuery.hasNextPage &&
          !assetsQuery.isFetching
        ) {
          void assetsQuery.fetchNextPage();
        }
      },
      { rootMargin: "800px" },
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, [assetsQuery]);

  const [lightboxIndex, setLightboxIndex] = useState<number | undefined>(
    undefined,
  );

  const hasFilters =
    filters.type !== undefined ||
    filters.city !== undefined ||
    filters.country !== undefined ||
    filters.includePaths.length > 0 ||
    filters.excludePaths.length > 0 ||
    filters.from !== undefined ||
    filters.until !== undefined;

  return (
    <div className="flex h-screen text-neutral-900">
      <Sidebar
        facets={facetsQuery.data}
        search={search}
        setFilters={setFilters}
        hasFilters={hasFilters}
      />

      <main className="flex min-w-0 flex-1 flex-col bg-neutral-50">
        <header className="flex items-baseline gap-3 border-b border-neutral-200 bg-white px-6 py-3">
          <h1 className="text-lg font-semibold">Timeline</h1>
          <span className="text-sm text-neutral-500">
            {assetsQuery.isPending
              ? "Loading…"
              : `${assets.length}${facetsQuery.data ? ` of ${facetsQuery.data.totalCount}` : ""} items`}
          </span>
          {assetsQuery.isFetching && (
            <span className="text-sm text-neutral-400">fetching…</span>
          )}
          {assetsQuery.isError && (
            <span className="text-sm text-red-600">
              {assetsQuery.error.message}
            </span>
          )}
        </header>

        <div className="flex-1 overflow-y-auto">
          {assetsQuery.isPending ? (
            <p className="p-8 text-neutral-500">Loading assets…</p>
          ) : days.length === 0 ? (
            <p className="p-8 text-neutral-500">
              No assets match the current filters.
            </p>
          ) : (
            <>
              {days.map((day) => (
                <section key={day.key}>
                  <div className="sticky top-0 z-10 flex items-baseline gap-2 border-b border-neutral-200 bg-white/85 px-4 py-2 backdrop-blur">
                    <h2 className="text-sm font-semibold">{day.label}</h2>
                    <span className="text-xs text-neutral-500">
                      {day.assets.length}
                    </span>
                  </div>
                  <div className="grid gap-0.5 p-0.5 [grid-template-columns:repeat(auto-fill,minmax(150px,1fr))]">
                    {day.assets.map((asset) => (
                      <AssetCell
                        key={asset.id}
                        asset={asset}
                        onClick={() =>
                          setLightboxIndex(
                            assets.findIndex((a) => a.id === asset.id),
                          )
                        }
                      />
                    ))}
                  </div>
                </section>
              ))}
              <div ref={sentinelRef} className="h-4" />
              {assetsQuery.isFetchingNextPage && (
                <p className="py-4 text-center text-sm text-neutral-500">
                  Loading more…
                </p>
              )}
            </>
          )}
        </div>
      </main>

      {lightboxIndex !== undefined && assets[lightboxIndex] && (
        <Lightbox
          asset={assets[lightboxIndex]}
          onClose={() => setLightboxIndex(undefined)}
          onPrev={
            lightboxIndex > 0
              ? () => setLightboxIndex(lightboxIndex - 1)
              : undefined
          }
          onNext={
            lightboxIndex < assets.length - 1
              ? () => setLightboxIndex(lightboxIndex + 1)
              : undefined
          }
        />
      )}
    </div>
  );
}

function AssetCell({ asset, onClick }: { asset: Asset; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="group relative aspect-square overflow-hidden bg-neutral-200 focus:outline-none"
    >
      <img
        src={thumbUrl(asset.checksum)}
        alt=""
        loading="lazy"
        className="h-full w-full object-cover transition group-hover:scale-105"
      />
      {asset.type === "video" && (
        <span className="absolute bottom-1 left-1 rounded bg-black/70 px-1.5 py-0.5 text-xs text-white">
          ▶ {formatDuration(asset.durationMs)}
        </span>
      )}
      {asset.isFavorite && (
        <span className="absolute top-1 right-1 text-sm drop-shadow">★</span>
      )}
    </button>
  );
}

function Lightbox({
  asset,
  onClose,
  onPrev,
  onNext,
}: {
  asset: Asset;
  onClose: () => void;
  onPrev?: () => void;
  onNext?: () => void;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowLeft") onPrev?.();
      if (e.key === "ArrowRight") onNext?.();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, onPrev, onNext]);

  return (
    <div
      className="fixed inset-0 z-50 flex flex-col bg-black/95"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div className="flex items-center justify-between px-4 py-3 text-sm text-neutral-300">
        <span>
          {[
            asset.localDateTime
              ? dayFormat.format(new Date(asset.localDateTime * 1000))
              : undefined,
            asset.city,
            asset.country,
          ]
            .filter(Boolean)
            .join(" · ")}
        </span>
        <button
          type="button"
          onClick={onClose}
          className="text-lg hover:text-white"
        >
          ✕
        </button>
      </div>
      <div className="relative flex min-h-0 flex-1 items-center justify-center px-14 pb-4">
        {onPrev && (
          <button
            type="button"
            onClick={onPrev}
            className="absolute left-2 text-3xl text-neutral-400 hover:text-white"
          >
            ‹
          </button>
        )}
        {asset.type === "video" ? (
          <video
            src={mediaUrl(asset.checksum)}
            controls
            autoPlay
            className="max-h-full max-w-full"
          />
        ) : (
          <img
            src={mediaUrl(asset.checksum)}
            alt=""
            className="max-h-full max-w-full object-contain"
          />
        )}
        {onNext && (
          <button
            type="button"
            onClick={onNext}
            className="absolute right-2 text-3xl text-neutral-400 hover:text-white"
          >
            ›
          </button>
        )}
      </div>
    </div>
  );
}

function PathInput({
  label,
  value,
  placeholder,
  onCommit,
}: {
  label: string;
  value: string[];
  placeholder?: string;
  onCommit: (paths: string[]) => void;
}) {
  const [text, setText] = useState(value.join(", "));
  useEffect(() => {
    setText(value.join(", "));
  }, [value.join(",")]);

  const commit = () => {
    const paths = text
      .split(",")
      .map((p) => p.trim())
      .filter(Boolean);
    setText(paths.join(", "));
    onCommit(paths);
  };

  return (
    <label className="mb-2 block last:mb-0">
      <span className="text-xs text-neutral-500">{label}</span>
      <input
        type="text"
        className="w-full rounded border border-neutral-300 px-2 py-1 text-sm"
        placeholder={placeholder}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            commit();
          }
        }}
      />
    </label>
  );
}

function Section({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="border-b border-neutral-200 px-4 py-3">
      <h3 className="mb-2 text-xs font-semibold tracking-wide text-neutral-500 uppercase">
        {title}
      </h3>
      {children}
    </div>
  );
}

function Sidebar({
  facets,
  search,
  setFilters,
  hasFilters,
}: {
  facets?: {
    countries: string[];
    cities: string[];
    paths: string[];
    totalCount: number;
  };
  search: AssetSearch;
  setFilters: (patch: Partial<AssetSearch>) => void;
  hasFilters: boolean;
}) {
  const includePaths = search.include ? search.include.split(",") : [];
  const excludePaths = search.exclude ? search.exclude.split(",") : [];

  return (
    <aside className="flex w-64 shrink-0 flex-col overflow-y-auto border-r border-neutral-200 bg-white">
      <div className="flex items-center justify-between border-b border-neutral-200 px-4 py-3">
        <span className="font-semibold">Filters</span>
        {hasFilters && (
          <button
            type="button"
            onClick={() =>
              setFilters({
                type: undefined,
                city: undefined,
                country: undefined,
                include: undefined,
                exclude: undefined,
                from: undefined,
                until: undefined,
              })
            }
            className="text-xs text-blue-600 hover:underline"
          >
            Clear all
          </button>
        )}
      </div>

      <Section title="Type">
        <div className="flex gap-1">
          {(
            [
              ["all", "All"],
              ["image", "Images"],
              ["video", "Videos"],
            ] as const
          ).map(([value, label]) => {
            const active =
              search.type === value || (value === "all" && !search.type);
            return (
              <button
                key={value}
                type="button"
                onClick={() =>
                  setFilters({ type: value === "all" ? undefined : value })
                }
                className={`flex-1 rounded border px-2 py-1 text-xs ${
                  active
                    ? "border-blue-600 bg-blue-600 text-white"
                    : "border-neutral-300 hover:bg-neutral-100"
                }`}
              >
                {label}
              </button>
            );
          })}
        </div>
      </Section>

      <Section title="Country">
        <select
          className="w-full rounded border border-neutral-300 px-2 py-1 text-sm"
          value={search.country ?? ""}
          onChange={(e) => setFilters({ country: e.target.value || undefined })}
        >
          <option value="">All countries</option>
          {facets?.countries.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
      </Section>

      <Section title="City">
        <select
          className="w-full rounded border border-neutral-300 px-2 py-1 text-sm"
          value={search.city ?? ""}
          onChange={(e) => setFilters({ city: e.target.value || undefined })}
        >
          <option value="">All cities</option>
          {facets?.cities.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
      </Section>

      <Section title="Date range">
        <div className="space-y-2 text-sm">
          <label className="block">
            <span className="text-xs text-neutral-500">From</span>
            <input
              type="date"
              className="w-full rounded border border-neutral-300 px-2 py-1"
              value={epochToDateInput(search.from)}
              onChange={(e) =>
                setFilters({
                  from: e.target.value
                    ? dateInputToEpoch(e.target.value)
                    : undefined,
                })
              }
            />
          </label>
          <label className="block">
            <span className="text-xs text-neutral-500">Until</span>
            <input
              type="date"
              className="w-full rounded border border-neutral-300 px-2 py-1"
              value={epochToDateInput(search.until)}
              onChange={(e) =>
                setFilters({
                  until: e.target.value
                    ? dateInputToEpoch(e.target.value, true)
                    : undefined,
                })
              }
            />
          </label>
        </div>
      </Section>

      <Section title="Folders">
        <PathInput
          label="Included"
          value={includePaths}
          placeholder="e.g. folder-a, folder-b"
          onCommit={(paths) =>
            setFilters({ include: paths.join(",") || undefined })
          }
        />
        <PathInput
          label="Excluded"
          value={excludePaths}
          placeholder="e.g. folder-c"
          onCommit={(paths) =>
            setFilters({ exclude: paths.join(",") || undefined })
          }
        />
      </Section>

      {facets && (
        <p className="px-4 py-3 text-xs text-neutral-500">
          {facets.totalCount} assets in library
        </p>
      )}
    </aside>
  );
}

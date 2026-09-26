import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  keepPreviousData,
  useInfiniteQuery,
  useQuery,
} from "@tanstack/react-query";
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  DownloadIcon,
  ImageIcon,
  PlayIcon,
  StarIcon,
  XIcon,
} from "lucide-react";
import {
  downloadUrl,
  fetchAssets,
  fetchFacets,
  fetchIndexStatus,
  mediaUrl,
  thumbUrl,
  type Asset,
  type AssetFilters,
  type IndexStatus,
} from "#/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarProvider,
  SidebarRail,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";

interface AssetSearch {
  context_query?: string;
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
  if (typeof search.context_query === "string" && search.context_query)
    s.context_query = search.context_query;
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
    contextQuery: search.context_query,
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
    const time = captureTime(a);
    const key = time ? toDateKey(new Date(time * 1000)) : "unknown";
    if (!current || current.key !== key) {
      current = {
        key,
        label:
          key === "unknown"
            ? "Unknown date"
            : dayFormat.format(new Date(time * 1000)),
        assets: [],
      };
      groups.push(current);
    }
    current.assets.push(a);
  }
  return groups;
}

/** The capture time to display: wall clock when known, else the UTC instant. */
function captureTime(a: Asset): number | undefined {
  return a.localDateTime ?? a.dateTime;
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
    search.context_query !== undefined ||
    filters.type !== undefined ||
    filters.city !== undefined ||
    filters.country !== undefined ||
    filters.includePaths.length > 0 ||
    filters.excludePaths.length > 0 ||
    filters.from !== undefined ||
    filters.until !== undefined;

  const similarityMode = search.context_query !== undefined;

  return (
    <SidebarProvider>
      <AppSidebar
        facets={facetsQuery.data}
        search={search}
        setFilters={setFilters}
        hasFilters={hasFilters}
      />

      <SidebarInset>
        <header className="flex items-center gap-3 px-6 py-3">
          <SidebarTrigger />
          <h1 className="text-lg font-semibold">
            {similarityMode
              ? `Similar to “${search.context_query}”`
              : "Timeline"}
          </h1>
          {assetsQuery.isPending ? (
            <Spinner className="text-muted-foreground" />
          ) : null}
          {assetsQuery.isFetching && (
            <Spinner className="text-muted-foreground" />
          )}
          {assetsQuery.isError && (
            <Badge variant="destructive">{assetsQuery.error.message}</Badge>
          )}
        </header>

        <div className="flex-1 overflow-y-auto">
          {assetsQuery.isPending ? (
            <div className="grid gap-0.5 px-6 [grid-template-columns:repeat(auto-fill,minmax(220px,1fr))]">
              {Array.from({ length: 24 }, (_, i) => (
                <Skeleton key={i} className="aspect-square" />
              ))}
            </div>
          ) : days.length === 0 ? (
            <Empty className="h-full">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <ImageIcon />
                </EmptyMedia>
                <EmptyTitle>No assets found</EmptyTitle>
                <EmptyDescription>
                  {similarityMode
                    ? "No assets match this search. Try a different query."
                    : "Nothing matches the current filters. Try adjusting or clearing them."}
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : similarityMode ? (
            <div className="px-6 pb-10">
              <div className="grid gap-0.5 [grid-template-columns:repeat(auto-fill,minmax(220px,1fr))]">
                {assets.map((asset, index) => (
                  <AssetCell
                    key={asset.id}
                    asset={asset}
                    onClick={() => setLightboxIndex(index)}
                  />
                ))}
              </div>
              <div ref={sentinelRef} className="h-4" />
            </div>
          ) : (
            <div className="space-y-10 pb-10">
              {days.map((day) => (
                <section key={day.key}>
                  <div className="sticky top-0 z-10 flex items-baseline gap-3 bg-background px-6 py-3">
                    <h2 className="text-xl font-semibold">{day.label}</h2>
                    <span className="text-sm text-muted-foreground">
                      {day.assets.length}
                    </span>
                  </div>
                  <div className="grid gap-0.5 px-6 [grid-template-columns:repeat(auto-fill,minmax(220px,1fr))]">
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
            </div>
          )}
          {assetsQuery.isFetchingNextPage && (
            <div className="flex justify-center py-6">
              <Spinner className="text-muted-foreground" />
            </div>
          )}
        </div>
      </SidebarInset>

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
    </SidebarProvider>
  );
}

function AssetCell({ asset, onClick }: { asset: Asset; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="group relative aspect-square overflow-hidden focus:outline-none"
    >
      <img
        src={thumbUrl(asset.checksum)}
        alt=""
        loading="lazy"
        className="h-full w-full object-cover transition group-hover:scale-105"
      />
      {asset.type === "video" && (
        <span className="absolute bottom-1 left-1 inline-flex items-center gap-1 rounded bg-black/70 px-1.5 py-0.5 text-xs text-white">
          <PlayIcon className="size-3" />
          {formatDuration(asset.durationMs)}
        </span>
      )}
      {asset.isFavorite && (
        <span className="absolute top-1 right-1 drop-shadow">
          <StarIcon className="size-4 fill-yellow-400 text-yellow-400" />
        </span>
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
      <div className="flex items-start justify-between gap-4 px-4 py-3 text-sm text-neutral-300">
        <div className="flex min-w-0 flex-col gap-1">
          <span>
            {[
              captureTime(asset)
                ? dayFormat.format(new Date(captureTime(asset)! * 1000))
                : undefined,
              asset.city,
              asset.country,
            ]
              .filter(Boolean)
              .join(" · ")}
          </span>
          {asset.paths && asset.paths.length > 0 && (
            <ul className="flex flex-col gap-0.5 font-mono text-xs break-all text-neutral-500">
              {asset.paths.map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          )}
        </div>
        <div className="flex items-center gap-1">
          <Button
            variant="ghost"
            size="icon"
            render={<a href={downloadUrl(asset.checksum)} download />}
            nativeButton={false}
            className="text-neutral-400 hover:bg-white/10 hover:text-white"
          >
            <DownloadIcon />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            onClick={onClose}
            className="text-neutral-400 hover:bg-white/10 hover:text-white"
          >
            <XIcon />
          </Button>
        </div>
      </div>
      <div
        className="relative flex min-h-0 flex-1 items-center justify-center px-14 pb-4"
        onClick={(e) => {
          if (e.target === e.currentTarget) onClose();
        }}
      >
        {onPrev && (
          <Button
            variant="ghost"
            size="icon"
            onClick={onPrev}
            className="absolute left-2 size-16 text-neutral-400 hover:bg-white/10 hover:text-white [&_svg:not([class*='size-'])]:size-10"
          >
            <ChevronLeftIcon />
          </Button>
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
          <Button
            variant="ghost"
            size="icon"
            onClick={onNext}
            className="absolute right-2 size-16 text-neutral-400 hover:bg-white/10 hover:text-white [&_svg:not([class*='size-'])]:size-10"
          >
            <ChevronRightIcon />
          </Button>
        )}
      </div>
    </div>
  );
}

function IndexerStats() {
  const statusQuery = useQuery({
    queryKey: ["indexStatus"],
    queryFn: ({ signal }) => fetchIndexStatus(signal),
    refetchInterval: (query) => {
      const phase = query.state.data?.phase;
      return phase === "indexing" || phase === "processing" ? 1000 : 5000;
    },
  });

  return (
    <div className="px-4 py-3">
      <Label className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">
        Indexer
      </Label>
      {statusQuery.isPending ? (
        <Spinner className="mt-2 text-muted-foreground" />
      ) : statusQuery.isError ? (
        <p className="mt-2 text-xs text-destructive">
          {statusQuery.error.message}
        </p>
      ) : statusQuery.data ? (
        <IndexerStatsBody status={statusQuery.data} />
      ) : null}
    </div>
  );
}

function formatEta(seconds: number) {
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${minutes % 60}m`;
}

function IndexerStatsBody({ status }: { status: IndexStatus }) {
  const percent =
    status.discovered > 0
      ? Math.min(100, Math.round((status.processed / status.discovered) * 100))
      : 0;
  const state =
    status.phase === "indexing"
      ? "Indexing"
      : status.phase === "processing"
        ? "Processing"
        : status.phase === "failed"
          ? "Failed"
          : "Completed";
  const showEta = status.etaSeconds !== undefined && status.etaSeconds > 0;

  return (
    <div className="mt-2 space-y-1.5">
      <div className="flex items-baseline justify-between gap-2 text-xs">
        <span className="text-muted-foreground">{state}</span>
        <span className="font-medium tabular-nums">
          {status.processed.toLocaleString()} /{" "}
          {status.discovered.toLocaleString()}
          {status.phase === "indexing" ? "+" : ""}
        </span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-muted">
        {status.phase !== "indexing" && (
          <div
            className={`h-full rounded-full transition-[width] duration-500 ${
              status.failed ? "bg-destructive" : "bg-primary"
            }`}
            style={{ width: `${percent}%` }}
          />
        )}
      </div>
      {showEta && (
        <p className="text-xs tabular-nums text-muted-foreground">
          ~{formatEta(status.etaSeconds!)}
          {status.phase === "indexing" ? "+" : ""} remaining
        </p>
      )}
      {status.error && (
        <p className="pt-1 text-xs break-words text-destructive">
          {status.error}
        </p>
      )}
    </div>
  );
}

function AppSidebar({
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

  const clearAll = () =>
    setFilters({
      context_query: undefined,
      type: undefined,
      city: undefined,
      country: undefined,
      include: undefined,
      exclude: undefined,
      from: undefined,
      until: undefined,
    });

  return (
    <Sidebar>
      <SidebarHeader>
        <div className="flex items-center justify-between px-2 py-1">
          <span className="font-semibold">Filters</span>
          {hasFilters && (
            <Button variant="ghost" size="xs" onClick={clearAll}>
              Clear all
            </Button>
          )}
        </div>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>Type</SidebarGroupLabel>
          <SidebarGroupContent>
            <ToggleGroup
              variant="outline"
              size="sm"
              spacing={4}
              value={search.type ? [search.type] : ["all"]}
              onValueChange={(value: string[]) =>
                setFilters({
                  type:
                    value[0] === "image" || value[0] === "video"
                      ? value[0]
                      : undefined,
                })
              }
              className="w-full"
            >
              <ToggleGroupItem value="all" className="flex-1 w-full">
                All
              </ToggleGroupItem>
              <ToggleGroupItem value="image" className="flex-1 w-full">
                Images
              </ToggleGroupItem>
              <ToggleGroupItem value="video" className="flex-1 w-full">
                Videos
              </ToggleGroupItem>
            </ToggleGroup>
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel>Country</SidebarGroupLabel>
          <SidebarGroupContent>
            <Select
              items={[
                { value: "", label: "All countries" },
                ...(facets?.countries ?? []).map((c) => ({
                  value: c,
                  label: c,
                })),
              ]}
              value={search.country ?? ""}
              onValueChange={(value) =>
                setFilters({ country: (value as string) || undefined })
              }
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">All countries</SelectItem>
                {facets?.countries.map((c) => (
                  <SelectItem key={c} value={c}>
                    {c}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel>City</SidebarGroupLabel>
          <SidebarGroupContent>
            <Select
              items={[
                { value: "", label: "All cities" },
                ...(facets?.cities ?? []).map((c) => ({ value: c, label: c })),
              ]}
              value={search.city ?? ""}
              onValueChange={(value) =>
                setFilters({ city: (value as string) || undefined })
              }
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">All cities</SelectItem>
                {facets?.cities.map((c) => (
                  <SelectItem key={c} value={c}>
                    {c}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel>Date range</SidebarGroupLabel>
          <SidebarGroupContent className="flex flex-col gap-2">
            <Label className="text-xs font-normal text-muted-foreground">
              From
            </Label>
            <Input
              type="date"
              value={epochToDateInput(search.from)}
              onChange={(e) =>
                setFilters({
                  from: e.target.value
                    ? dateInputToEpoch(e.target.value)
                    : undefined,
                })
              }
            />
            <Label className="text-xs font-normal text-muted-foreground">
              Until
            </Label>
            <Input
              type="date"
              value={epochToDateInput(search.until)}
              onChange={(e) =>
                setFilters({
                  until: e.target.value
                    ? dateInputToEpoch(e.target.value, true)
                    : undefined,
                })
              }
            />
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel>Paths</SidebarGroupLabel>
          <SidebarGroupContent className="flex flex-col gap-2">
            <PathInput
              label="Includes"
              value={includePaths}
              placeholder={"path/*"}
              onCommit={(paths) =>
                setFilters({ include: paths.join(",") || undefined })
              }
            />
            <PathInput
              label="Excludes"
              value={excludePaths}
              placeholder={"path/*"}
              onCommit={(paths) =>
                setFilters({ exclude: paths.join(",") || undefined })
              }
            />
          </SidebarGroupContent>
        </SidebarGroup>

        {facets && (
          <p className="px-6 py-3 text-xs text-muted-foreground">
            {facets.totalCount} assets in library
          </p>
        )}

        <SidebarGroup>
          <SidebarGroupLabel>Search</SidebarGroupLabel>
          <SidebarGroupContent>
            <SearchInput
              value={search.context_query ?? ""}
              onCommit={(v) => setFilters({ context_query: v || undefined })}
            />
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter>
        <IndexerStats />
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  );
}

function SearchInput({
  value,
  onCommit,
}: {
  value: string;
  onCommit: (value: string) => void;
}) {
  const [text, setText] = useState(value);
  useEffect(() => {
    setText(value);
  }, [value]);

  const commit = () => onCommit(text.trim());

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        commit();
      }}
    >
      <Input
        type="search"
        placeholder="Describe what to find…"
        value={text}
        onChange={(e) => setText(e.target.value)}
        onBlur={commit}
      />
    </form>
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
  const [text, setText] = useState(value.join("\n"));
  useEffect(() => {
    setText(value.join("\n"));
  }, [value.join("\n")]);

  const commit = () => {
    const paths = text
      .split("\n")
      .map((p) => p.trim())
      .filter(Boolean);
    setText(paths.join("\n"));
    onCommit(paths);
  };

  return (
    <Label className="flex-col items-start gap-1.5">
      <span className="text-xs font-normal text-muted-foreground">{label}</span>
      <Textarea
        rows={2}
        className="text-xs md:text-xs"
        placeholder={placeholder}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            commit();
          }
        }}
      />
    </Label>
  );
}

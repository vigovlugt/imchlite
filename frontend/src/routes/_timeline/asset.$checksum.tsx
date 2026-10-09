import { useCallback, useEffect, useMemo } from "react";
import { createFileRoute, useRouter } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { fetchAsset } from "#/lib/api";
import { filtersFromSearch, useAssetsQuery } from "#/lib/search";
import { Lightbox } from "@/components/lightbox";
import { Spinner } from "@/components/ui/spinner";

// The asset viewer, opened on top of the timeline with the timeline's
// filters kept in the search params so prev/next step through its results.
export const Route = createFileRoute("/_timeline/asset/$checksum")({
  component: AssetViewer,
});

function AssetViewer() {
  const { checksum } = Route.useParams();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const router = useRouter();
  const filters = useMemo(() => filtersFromSearch(search), [search]);

  const assetsQuery = useAssetsQuery(filters);
  const assets = useMemo(
    () => assetsQuery.data?.pages.flatMap((p) => p.assets) ?? [],
    [assetsQuery.data],
  );
  const index = assets.findIndex((a) => a.checksum === checksum);

  // An asset opened by url may not be on a loaded page, or not match the
  // filters at all; load it on its own then, without prev/next.
  const assetQuery = useQuery({
    queryKey: ["asset", checksum],
    queryFn: ({ signal }) => fetchAsset(checksum, signal),
    enabled: index < 0,
  });
  const asset = index >= 0 ? assets[index] : assetQuery.data;

  // Load the next page before stepping onto the last loaded asset.
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = assetsQuery;
  useEffect(() => {
    if (
      index >= 0 &&
      index >= assets.length - 2 &&
      hasNextPage &&
      !isFetchingNextPage
    ) {
      void fetchNextPage();
    }
  }, [index, assets.length, hasNextPage, isFetchingNextPage, fetchNextPage]);

  // Stepping replaces the history entry, so back closes the viewer instead
  // of stepping back through every asset seen.
  const goTo = useCallback(
    (target: string) =>
      void navigate({
        to: "/asset/$checksum",
        params: { checksum: target },
        search: (prev) => prev,
        replace: true,
      }),
    [navigate],
  );

  // Go back when the viewer was opened from the timeline, so the timeline's
  // history entry is reused; a viewer opened by url has nothing to go back to.
  const onClose = useCallback(() => {
    if (router.history.canGoBack()) router.history.back();
    else void navigate({ to: "/", search: (prev) => prev });
  }, [router, navigate]);

  return (
    <Lightbox
      asset={asset}
      placeholder={
        assetQuery.isError ? (
          <p className="text-sm text-neutral-400">{assetQuery.error.message}</p>
        ) : (
          <Spinner className="text-neutral-400" />
        )
      }
      onClose={onClose}
      onPrev={index > 0 ? () => goTo(assets[index - 1].checksum) : undefined}
      onNext={
        index >= 0 && index < assets.length - 1
          ? () => goTo(assets[index + 1].checksum)
          : undefined
      }
    />
  );
}

import { useEffect, type ReactNode } from "react";
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  DownloadIcon,
  SparklesIcon,
  XIcon,
} from "lucide-react";
import {
  downloadUrl,
  mediaUrl,
  previewUrl,
  type Asset,
} from "#/lib/api";
import { captureTime, dayFormat } from "#/lib/format";
import { Button } from "@/components/ui/button";

/**
 * A full screen viewer for one asset. Without an asset it shows the
 * placeholder instead, e.g. while the asset loads.
 */
export function Lightbox({
  asset,
  placeholder,
  onClose,
  onFindSimilar,
  onPrev,
  onNext,
}: {
  asset?: Asset;
  placeholder?: ReactNode;
  onClose: () => void;
  onFindSimilar?: () => void;
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
          {asset && (
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
          )}
          {asset?.paths && asset.paths.length > 0 && (
            <ul className="flex flex-col gap-0.5 font-mono text-xs break-all text-neutral-500">
              {asset.paths.map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          )}
        </div>
        <div className="flex items-center gap-1">
          {asset && onFindSimilar && (
            <Button
              variant="ghost"
              size="icon"
              title="Find similar"
              aria-label="Find similar"
              onClick={onFindSimilar}
              className="text-neutral-400 hover:bg-white/10 hover:text-white"
            >
              <SparklesIcon />
            </Button>
          )}
          {asset && (
            <Button
              variant="ghost"
              size="icon"
              render={<a href={downloadUrl(asset.checksum)} download />}
              nativeButton={false}
              className="text-neutral-400 hover:bg-white/10 hover:text-white"
            >
              <DownloadIcon />
            </Button>
          )}
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
        {!asset ? (
          placeholder
        ) : asset.type === "video" ? (
          <video
            src={mediaUrl(asset.checksum)}
            controls
            autoPlay
            className="max-h-full max-w-full"
          />
        ) : (
          <img
            src={previewUrl(asset.checksum)}
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

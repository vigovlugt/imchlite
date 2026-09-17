set -Eeuo pipefail

(
  cd frontend
  bun install --frozen-lockfile
  bun run build
)

go build .

echo "Built imchlite"

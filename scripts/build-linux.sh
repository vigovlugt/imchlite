set -Eeuo pipefail

cd frontend
bun run build
cd ..

go build .

echo "Built imchlite"
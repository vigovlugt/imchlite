#!/bin/sh
# Builds the vec1 SQLite extension binaries embedded by internal/vec1.
#
# Sources (version-0.7):
#   https://sqlite.org/vec1/raw/vec1.c?ci=version-0.7
#   https://sqlite.org/download.html  (sqlite3.h + sqlite3ext.h amalgamation)
#
# Linux builds the multi-arch configuration from the upstream Makefile: two
# objects, one scalar and one AVX2, with a runtime CPU check choosing between
# them. Windows has no scalar/AVX2 dispatch under mingw-w64 (clang's
# __builtin_cpu_supports needs libgcc symbols mingw lacks), so it builds a
# plain scalar binary.
set -eu

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

curl -sL -o "$work/vec1.c" "https://sqlite.org/vec1/raw/vec1.c?ci=version-0.7"
curl -sL -o "$work/sqlite3.zip" "https://sqlite.org/2026/sqlite-amalgamation-3530400.zip"
unzip -q -j -o "$work/sqlite3.zip" "*/sqlite3.h" "*/sqlite3ext.h" -d "$work"

cd "$work"

# linux/amd64: multi-arch with runtime AVX2 detection.
cc -O3 -DNDEBUG -DVEC1SIMD=SCALAR -c vec1.c -o vec1scalar.o -fPIC
cc -O3 -DNDEBUG -DVEC1SIMD=AVX2 -mavx2 -mfma -c vec1.c -o vec1avx2.o -fPIC
cc vec1scalar.o vec1avx2.o -o vec1.so -shared -fPIC -lm -lpthread

# windows/amd64: scalar only, built with zig cc.
zig cc -target x86_64-windows-gnu -O3 -DNDEBUG -shared -o vec1.dll vec1.c

repo="$(cd "$(dirname "$0")/../.." && pwd)"
cp vec1.so "$repo/internal/vec1/bin/linux/vec1.so"
cp vec1.dll "$repo/internal/vec1/bin/windows/vec1.dll"
echo "Built internal/vec1/bin/linux/vec1.so and internal/vec1/bin/windows/vec1.dll"

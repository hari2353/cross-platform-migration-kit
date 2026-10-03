#!/bin/sh
set -eu

root="$(mktemp -d)"
trap 'rm -rf "$root"' EXIT

mkdir -p "$root/source/Documents"
printf 'docker portability test\n' > "$root/source/Documents/note.txt"

/out/migrate keygen --output "$root/migration.key"
/out/migrate capture \
  --source "$root/source" \
  --output "$root/repository" \
  --key-file "$root/migration.key"
/out/migrate verify \
  --bundle "$root/repository" \
  --key-file "$root/migration.key"

/out/migrate restore \
  --bundle "$root/repository" \
  --destination "$root/destination" \
  --key-file "$root/migration.key" \
  --dry-run

if [ -e "$root/destination/Documents/note.txt" ]; then
  echo "dry-run created a destination file" >&2
  exit 1
fi

/out/migrate restore \
  --bundle "$root/repository" \
  --destination "$root/destination" \
  --key-file "$root/migration.key"

cmp "$root/source/Documents/note.txt" "$root/destination/Documents/note.txt"

/out/migrate verify \
  --bundle "$root/repository" \
  --key-file "$root/migration.key" \
  --destination "$root/destination"

/out/migrate resume \
  --bundle "$root/repository" \
  --key-file "$root/migration.key"

/out/migrate doctor \
  --bundle "$root/repository" \
  --key-file "$root/migration.key" \
  --destination "$root/destination"

echo "docker portability smoke test passed"

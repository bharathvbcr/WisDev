#!/usr/bin/env bash
set -euo pipefail

INPUT_COVERAGE="${1:-coverage.out}"
OUTPUT_COVERAGE="${2:-coverage.non_generated.out}"
MIN_COVERAGE="${3:-}"

if [[ ! -f "$INPUT_COVERAGE" ]]; then
  echo "Coverage input file not found: $INPUT_COVERAGE" >&2
  exit 1
fi

is_generated_file() {
  local raw_path="$1"
  local normalized_path="${raw_path//\\//}"

  local filename
  filename="$(basename "$normalized_path")"

  case "$filename" in
    *_test.go|auto_coverage_test.go)
      return 0
      ;;
  esac

  case "$normalized_path" in
    *.pb.go|*_grpc.pb.go)
      return 0
      ;;
  esac

  local candidate
  for candidate in \
    "$normalized_path" \
    "./$normalized_path" \
    "${normalized_path#*orchestrator/}" \
    "$(pwd)/$normalized_path"
  do
    if [[ -n "$candidate" && -f "$candidate" ]]; then
      if grep -qE "^// Code generated .*DO NOT EDIT\\." "$candidate"; then
        return 0
      fi
      return 1
    fi
  done

  # If we cannot resolve a source file, treat it as generated to avoid inflating coverage.
  return 0
}

tmp_file="$(mktemp)"
trap 'rm -f "$tmp_file"' EXIT

if ! read -r first_line < "$INPUT_COVERAGE"; then
  echo "Failed to parse coverage profile: $INPUT_COVERAGE" >&2
  exit 1
fi

if [[ "$first_line" != mode:* ]]; then
  echo "Invalid coverage profile header: $first_line" >&2
  exit 1
fi

{
  echo "$first_line" > "$tmp_file"

  tail -n +2 "$INPUT_COVERAGE" | while IFS= read -r line; do
    [[ -z "$line" ]] && continue

    file_path="${line%%:*}"
    if is_generated_file "$file_path"; then
      continue
    fi

    echo "$line" >> "$tmp_file"
  done
} || {
  echo "Failed to parse coverage profile: $INPUT_COVERAGE" >&2
  exit 1
}

mv "$tmp_file" "$OUTPUT_COVERAGE"
trap - EXIT

if [[ ! -s "$OUTPUT_COVERAGE" ]] || [[ "$(wc -l < "$OUTPUT_COVERAGE")" -eq 1 ]]; then
  echo "No non-generated coverage entries found after filtering." >&2
  exit 1
fi

coverage_line="$(go tool cover -func="$OUTPUT_COVERAGE" | tail -n 1 || true)"
if [[ -z "$coverage_line" ]]; then
  echo "Unable to compute coverage from filtered profile." >&2
  exit 1
fi

coverage_pct="${coverage_line##* }"
coverage_pct="${coverage_pct%\%}"
echo "Non-generated coverage: ${coverage_pct}%"

if [[ -n "$MIN_COVERAGE" ]]; then
  if ! awk -v actual="$coverage_pct" -v minimum="$MIN_COVERAGE" 'BEGIN { exit !(actual + 0 >= minimum + 0) }'; then
    echo "Coverage check failed. Non-generated coverage ${coverage_pct}% is below required ${MIN_COVERAGE}%." >&2
    exit 1
  fi
fi

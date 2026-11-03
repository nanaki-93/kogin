#!/usr/bin/env bash
# bundle-codebase.sh
# Serializes a repository or directory into a single structured Markdown file
# optimized for LLM context windows (Gemini prompt caching, Claude, etc.)

set -euo pipefail

OUTPUT_FILE="${1:-codebase-bundle.md}"
TARGET_DIR="${2:-.}"
MAX_FILE_SIZE_KB="${MAX_FILE_SIZE_KB:-500}"

# Resolve target directory to absolute path
TARGET_DIR="$(cd "$TARGET_DIR" && pwd)"

# Temporary directory for building the bundle
TEMP_DIR="$(mktemp -d)"
TREE_FILE="$TEMP_DIR/tree.txt"
CONTENT_FILE="$TEMP_DIR/content.md"
trap 'rm -rf "$TEMP_DIR"' EXIT

echo "==> Bundling codebase from: $TARGET_DIR"
echo "==> Output destination: $OUTPUT_FILE"

# Map common file extensions to markdown code fence languages
get_lang() {
    local ext="${1##*.}"
    case "$ext" in
        go) echo "go" ;;
        py) echo "python" ;;
        sh|bash|zsh) echo "bash" ;;
        js|mjs|cjs) echo "javascript" ;;
        ts|mts|cts) echo "typescript" ;;
        jsx) echo "jsx" ;;
        tsx) echo "tsx" ;;
        json) echo "json" ;;
        yaml|yml) echo "yaml" ;;
        toml) echo "toml" ;;
        md|markdown) echo "markdown" ;;
        html) echo "html" ;;
        css|scss|sass) echo "css" ;;
        sql) echo "sql" ;;
        rs) echo "rust" ;;
        c|h) echo "c" ;;
        cpp|cc|cxx|hpp) echo "cpp" ;;
        java) echo "java" ;;
        kt|kts) echo "kotlin" ;;
        swift) echo "swift" ;;
        rb) echo "ruby" ;;
        php) echo "php" ;;
        xml) echo "xml" ;;
        proto) echo "protobuf" ;;
        dockerfile|Dockerfile) echo "dockerfile" ;;
        makefile|Makefile) echo "makefile" ;;
        *) echo "" ;;
    esac
}

# Check if a file is binary
is_binary() {
    local filepath="$1"
    # Grep check: returns 1 if binary
    if ! grep -Iq . "$filepath" 2>/dev/null; then
        return 0
    fi
    return 1
}

# Collect files
cd "$TARGET_DIR"
FILES=()

if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "==> Detected Git repository. Using git ls-files (respecting .gitignore)..."
    while IFS= read -r file; do
        [[ -n "$file" ]] && FILES+=("$file")
    done < <(git ls-files -co --exclude-standard)
else
    echo "==> Not a Git repository. Finding files while ignoring standard non-code dirs..."
    while IFS= read -r file; do
        file="${file#./}"
        [[ -n "$file" ]] && FILES+=("$file")
    done < <(find . -type f \
        -not -path '*/.*' \
        -not -path '*/node_modules/*' \
        -not -path '*/vendor/*' \
        -not -path '*/build/*' \
        -not -path '*/dist/*' \
        -not -path '*/__pycache__/*' \
        -not -path '*/.git/*')
fi

# Filter out self (the output bundle), binary files, and large files
INCLUDED_FILES=()
SKIPPED_FILES=()

for rel_path in "${FILES[@]}"; do
    full_path="$TARGET_DIR/$rel_path"

    # Don't bundle the output file itself
    if [[ "$full_path" == "$(pwd)/$OUTPUT_FILE" ]] || [[ "$rel_path" == "$OUTPUT_FILE" ]] || [[ "$rel_path" == "codebase-bundle.md" ]]; then
        continue
    fi

    # Never bundle local secrets, runtime configs, or generated/worktree state
    case "$rel_path" in
        .env|.env.*|*/.env|*/.env.*|config.yaml|*/config.yaml|.kogin/*|*/.kogin/*|build/*|*/build/*|.worktrees/*|*/.worktrees/*)
            SKIPPED_FILES+=("$rel_path (excluded sensitive or generated path)")
            continue
            ;;
    esac

    # Skip files that don't exist (e.g. deleted or broken symlinks)
    if [[ ! -f "$full_path" ]]; then
        continue
    fi

    # Check file size
    file_size_kb=$(awk "BEGIN {print int($(wc -c < "$full_path") / 1024)}")
    if (( file_size_kb > MAX_FILE_SIZE_KB )); then
        SKIPPED_FILES+=("$rel_path (size: ${file_size_kb}KB > limit: ${MAX_FILE_SIZE_KB}KB)")
        continue
    fi

    # Skip binary files
    if is_binary "$full_path"; then
        SKIPPED_FILES+=("$rel_path (binary file)")
        continue
    fi

    INCLUDED_FILES+=("$rel_path")
done

echo "==> Found ${#INCLUDED_FILES[@]} text files to bundle (${#SKIPPED_FILES[@]} skipped)."

# Generate Directory Tree
echo "==> Building directory tree..."
{
    echo "# Directory Structure"
    echo ""
    echo '```text'
    for f in "${INCLUDED_FILES[@]}"; do
        echo "$f"
    done
    echo '```'
    echo ""
} > "$TREE_FILE"

# Process each file into Markdown
echo "==> Serializing file contents..."
{
    for f in "${INCLUDED_FILES[@]}"; do
        full_path="$TARGET_DIR/$f"
        lang=$(get_lang "$f")

        echo "## File: \`$f\`"
        echo ""
        echo '````'"$lang"
        cat "$full_path"
        echo ""
        echo '````'
        echo ""
    done
} > "$CONTENT_FILE"

# Assemble the final Markdown document
{
    echo "# Codebase Bundle"
    echo ""
    echo "- **Source Directory:** \`$TARGET_DIR\`"
    echo "- **Generated At:** $(date -u '+%Y-%m-%d %H:%M:%SZ')"
    echo "- **Total Included Files:** ${#INCLUDED_FILES[@]}"
    echo "- **Total Skipped Files:** ${#SKIPPED_FILES[@]}"
    echo ""
    if (( ${#SKIPPED_FILES[@]} > 0 )); then
        echo "<details>"
        echo "<summary>Skipped Files (${#SKIPPED_FILES[@]})</summary>"
        echo ""
        for s in "${SKIPPED_FILES[@]}"; do
            echo "- $s"
        done
        echo ""
        echo "</details>"
        echo ""
    fi
    echo "---"
    echo ""
    cat "$TREE_FILE"
    echo "---"
    echo ""
    echo "# File Contents"
    echo ""
    cat "$CONTENT_FILE"
} > "$OUTPUT_FILE"

# Stats calculation
TOTAL_BYTES=$(wc -c < "$OUTPUT_FILE" | tr -d ' ')
TOTAL_LINES=$(wc -l < "$OUTPUT_FILE" | tr -d ' ')
EST_TOKENS=$(( TOTAL_BYTES / 4 ))

echo "============================================================"
echo " Bundle created successfully: $OUTPUT_FILE"
echo " Total files:   ${#INCLUDED_FILES[@]}"
echo " Total lines:   $TOTAL_LINES"
echo " Total size:    $(( TOTAL_BYTES / 1024 )) KB"
echo " Approx tokens: ~$EST_TOKENS tokens"
echo "============================================================"
echo "You can now drag and drop '$OUTPUT_FILE' into Gemini chat with prompt caching."

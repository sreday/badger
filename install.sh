#!/bin/sh
# Badger 2.0 installer for Mac and Linux:
#
#   curl -fsSL https://raw.githubusercontent.com/sreday/badger/main/install.sh | sh
#
# Removes the old Badger (badger-darwin-* / badger-linux-* downloads; to the
# Trash on a Mac), keeps its printer settings, installs badger2 and opens it
# at http://localhost:8090. Run it again to update.
set -e

REPO="sreday/badger"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "Sorry, this installer is for Mac and Linux. On Windows use install.ps1."; exit 1 ;;
esac
case "$(uname -m)" in
  arm64 | aarch64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) echo "Sorry, unsupported processor: $(uname -m)"; exit 1 ;;
esac
asset="badger2-$os-$arch"

echo "Badger 2.0 installer"

# The newest release with a Badger 2.0 build (pre-releases included).
url=$(curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=30" |
  grep -o 'https://[^"]*/'"$asset"'"' | head -1 | tr -d '"')
if [ -z "$url" ]; then
  echo "Couldn't find a Badger 2.0 download for $os/$arch. Ask Mark or Miko."
  exit 1
fi

mkdir -p "$HOME/.badger"

# --- Remove the old Badger -------------------------------------------------
for dir in "$HOME/Downloads" "$HOME/Desktop" "$HOME" "$HOME/bin" "$HOME/.local/bin" /usr/local/bin; do
  for f in "$dir"/badger-darwin-* "$dir"/badger-linux-*; do
    [ -f "$f" ] || continue
    # Only Badger itself (other tools are also called badger).
    grep -q "sreday/badger" "$f" 2>/dev/null || continue
    # Keep its printer settings for 2.0.
    if [ -f "$dir/config.json" ] && [ ! -f "$HOME/.badger/config.json" ]; then
      cp "$dir/config.json" "$HOME/.badger/config.json"
      echo "Kept your printer settings from $dir/config.json"
    fi
    pkill -f "$(basename "$f")" 2>/dev/null || true
    if [ "$os" = darwin ] && [ -d "$HOME/.Trash" ]; then
      mv "$f" "$HOME/.Trash/" 2>/dev/null || rm -f "$f"
      echo "Moved the old Badger to the Trash: $f"
    else
      rm -f "$f"
      echo "Removed the old Badger: $f"
    fi
  done
done

# --- Install Badger 2.0 ----------------------------------------------------
if [ -w /usr/local/bin ]; then
  bindir=/usr/local/bin
else
  bindir="$HOME/.local/bin"
  mkdir -p "$bindir"
  case ":$PATH:" in
    *":$bindir:"*) ;;
    *)
      for rc in "$HOME/.zshrc" "$HOME/.bashrc"; do
        if [ -f "$rc" ] || [ "$rc" = "$HOME/.zshrc" -a "$os" = darwin ]; then
          grep -q '.local/bin' "$rc" 2>/dev/null || echo 'export PATH="$HOME/.local/bin:$PATH"' >>"$rc"
        fi
      done
      PATH="$bindir:$PATH"
      ;;
  esac
fi

echo "Downloading $url"
tmp="$bindir/.badger2.download"
curl -fsSL "$url" -o "$tmp"
chmod +x "$tmp"
mv -f "$tmp" "$bindir/badger2"
echo "Installed: $bindir/badger2 ($("$bindir/badger2" version 2>/dev/null || echo badger2))"
echo
echo "Opening Badger 2.0 at http://localhost:8090"
echo "Keep this window open while you print. Next time, just type: badger2"
echo
exec "$bindir/badger2"

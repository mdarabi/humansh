#!/bin/sh
set -eu

install_dir="$HOME/.local/bin"
binary="$install_dir/humansh"
mode=release
target_shell=
while [ "$#" -gt 0 ]; do
  case $1 in
    --local)
      [ "$mode" = release ] || { echo "usage: scripts/install.sh [--local] [--shell bash|zsh]" >&2; exit 2; }
      mode=local
      shift ;;
    --shell)
      if [ -n "$target_shell" ] || [ "$#" -lt 2 ]; then
        echo "usage: scripts/install.sh [--local] [--shell bash|zsh]" >&2
        exit 2
      fi
      target_shell=$2
      case $target_shell in bash|zsh) ;; *) echo "humansh installer: --shell must be bash or zsh." >&2; exit 2 ;; esac
      shift 2 ;;
    *) echo "usage: scripts/install.sh [--local] [--shell bash|zsh]" >&2; exit 2 ;;
  esac
done

mkdir -p "$install_dir"
temp_dir=
binary_temp=
previous_binary=
binary_replaced=0
install_committed=0
cleanup() {
  if [ "$binary_replaced" -eq 1 ] && [ "$install_committed" -eq 0 ]; then
    if [ -n "$previous_binary" ] && [ -e "$previous_binary" ]; then
      mv -f "$previous_binary" "$binary"
      previous_binary=
    else
      rm -f "$binary"
    fi
    binary_replaced=0
  fi
  if [ -n "$binary_temp" ]; then
    rm -f "$binary_temp"
    binary_temp=
  fi
  if [ -n "$previous_binary" ]; then
    rm -f "$previous_binary"
    previous_binary=
  fi
  if [ -n "$temp_dir" ]; then
    rm -rf "$temp_dir"
    temp_dir=
  fi
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

if [ "$mode" = local ]; then
  script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
  repo_dir=$(dirname -- "$script_dir")
  temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/humansh-install.XXXXXX")
  (cd "$repo_dir" && go build -trimpath -o "$temp_dir/humansh" ./cmd/humansh)
  source_binary="$temp_dir/humansh"
else
  repository=${HUMANSH_REPOSITORY:-agenticlab-ai/humansh}
  repository_owner=${repository%/*}
  repository_name=${repository#*/}
  if [ "$repository" != "$repository_owner/$repository_name" ] || [ -z "$repository_owner" ] || [ -z "$repository_name" ]; then
    echo "humansh installer: HUMANSH_REPOSITORY must have the form owner/name." >&2
    exit 1
  fi
  case $repository_owner$repository_name in *[!0-9A-Za-z._-]*) echo "humansh installer: HUMANSH_REPOSITORY contains unsupported characters." >&2; exit 1 ;; esac
  case $(uname -s) in Darwin) os=darwin ;; Linux) os=linux ;; *) echo "unsupported operating system" >&2; exit 1 ;; esac
  case $(uname -m) in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) echo "unsupported architecture" >&2; exit 1 ;; esac
  temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/humansh-install.XXXXXX")
  base="https://github.com/$repository/releases/latest/download"
  asset="humansh-$os-$arch.tar.gz"
  curl -fL --proto '=https' --tlsv1.2 "$base/$asset" -o "$temp_dir/$asset"
  curl -fL --proto '=https' --tlsv1.2 "$base/$asset.sha256" -o "$temp_dir/$asset.sha256"
  expected=$(awk -v asset="$asset" '
    NF == 2 && $2 == asset && length($1) == 64 && $1 !~ /[^0-9A-Fa-f]/ {
      if (found) exit 2
      print tolower($1)
      found = 1
    }
    END { if (!found) exit 1 }
  ' "$temp_dir/$asset.sha256") || { echo "invalid checksum manifest" >&2; exit 1; }
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$temp_dir/$asset" | awk '{print $1}')
  else
    actual=$(shasum -a 256 "$temp_dir/$asset" | awk '{print $1}')
  fi
  [ "$expected" = "$actual" ] || { echo "checksum verification failed" >&2; exit 1; }
  members=$(tar -tzf "$temp_dir/$asset")
  [ "$members" = "humansh" ] || { echo "release archive contains unexpected paths" >&2; exit 1; }
  tar -xzf "$temp_dir/$asset" -C "$temp_dir"
  if [ ! -f "$temp_dir/humansh" ] || [ -L "$temp_dir/humansh" ]; then
    echo "release archive does not contain a regular humansh binary" >&2
    exit 1
  fi
  source_binary="$temp_dir/humansh"
fi

binary_temp=$(mktemp "$install_dir/.humansh-install.XXXXXX")
install -m 0755 "$source_binary" "$binary_temp"
if [ -e "$binary" ] || [ -L "$binary" ]; then
  if [ ! -f "$binary" ] && [ ! -L "$binary" ]; then
    echo "humansh installer: $binary is not a regular file or symlink; no replacement was made." >&2
    exit 1
  fi
  previous_binary=$(mktemp "$install_dir/.humansh-previous.XXXXXX")
  rm -f "$previous_binary"
  mv "$binary" "$previous_binary"
fi
mv "$binary_temp" "$binary"
binary_temp=
binary_replaced=1

setup_status=0
setup_hint="$binary setup"
[ -z "$target_shell" ] || setup_hint="$setup_hint --shell $target_shell"
if [ "${HUMANSH_NONINTERACTIVE:-0}" = 1 ]; then
  echo "Run '$setup_hint' from a terminal to finish setup."
elif [ -t 0 ]; then
	if [ -n "$target_shell" ]; then
		if "$binary" setup --shell "$target_shell"; then :; else setup_status=$?; fi
	else
		if "$binary" setup; then :; else setup_status=$?; fi
	fi
elif (: </dev/tty) 2>/dev/null; then
	if [ -n "$target_shell" ]; then
		if "$binary" setup --shell "$target_shell" </dev/tty >/dev/tty 2>/dev/tty; then :; else setup_status=$?; fi
	else
		if "$binary" setup </dev/tty >/dev/tty 2>/dev/tty; then :; else setup_status=$?; fi
	fi
else
  echo "Run '$setup_hint' from a terminal to finish setup."
fi
binary_restored=0
if [ ! -e "$binary" ] && [ ! -L "$binary" ]; then
	binary_temp=$(mktemp "$install_dir/.humansh-install.XXXXXX")
	install -m 0755 "$source_binary" "$binary_temp"
	if [ -e "$binary" ] || [ -L "$binary" ]; then
		echo "humansh installer: $binary reappeared unexpectedly during recovery; refusing to overwrite it." >&2
		exit 1
	fi
	mv "$binary_temp" "$binary"
	binary_temp=
	binary_restored=1
fi
if [ ! -f "$binary" ] || [ -L "$binary" ] || [ ! -x "$binary" ] || ! cmp -s "$source_binary" "$binary"; then
	echo "humansh installer: the installed binary changed unexpectedly during setup; rolling back the binary installation." >&2
	exit 1
fi
if [ "$binary_restored" -eq 1 ]; then
	echo "humansh installer: the installed binary disappeared during setup and was restored."
fi
if [ "$setup_status" -ne 0 ]; then
	if [ "$setup_status" -eq 21 ]; then
		printf '\nInstallation stopped\n\n'
		printf '  Fix the provider issue above, then run the installer again.\n'
		exit 0
	fi
	echo "humansh installer: setup did not complete; rolling back the binary installation." >&2
	exit "$setup_status"
fi
install_committed=1
[ -z "$previous_binary" ] || rm -f "$previous_binary"
previous_binary=
printf '\nInstallation details\n\n'
printf '  %-10s %s%s\n' 'Binary' '~' '/.local/bin/humansh'
printf '  %-10s %s\n' 'License' 'MIT'

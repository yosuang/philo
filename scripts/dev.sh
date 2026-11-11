#!/usr/bin/env bash
# Start the local Postgres dependency, the Go server, and the native desktop.
# mise's depends and wait_for wait until a task exits, and its daemons keep
# running after the task exits, so this script owns the order and the cleanup.
set +x
set -euo pipefail

root=""
run_dir=""
lock_dir=""
server_pid=""
desktop_pid=""
watcher_pid=""
postgres_started=0
cleaning=0

finish() {
  local status="${1:-$?}"
  if [[ "$cleaning" == "1" ]]; then
    return 0
  fi
  cleaning=1
  trap - EXIT INT TERM HUP
  set +e
  stop_watcher
  stop_desktop
  stop_server
  if [[ "$postgres_started" == "1" ]]; then
    # Stop only the Postgres container this run started. Do not remove it
    # and do not delete the named volume.
    compose stop postgres >/dev/null 2>&1 || true
  fi
  release_lock
  if [[ -n "$run_dir" && -d "$run_dir" ]]; then
    rm -rf "$run_dir"
  fi
  exit "$status"
}

main() {
  umask 077
  root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
  cd -- "$root"
  acquire_lock
  trap 'finish $?' EXIT
  trap 'finish 130' INT
  trap 'finish 143' TERM
  trap 'finish 129' HUP
  run_dir=$(mktemp -d "${TMPDIR:-/tmp}/philo-mise-dev.XXXXXX")
  chmod 700 "$run_dir"

  if ! preflight; then
    exit 1
  fi
  resolve_auth
  start_postgres
  start_server
  wait_for_server
  start_desktop
}

acquire_lock() {
  local id old cmd
  id=$(printf '%s' "$root" | cksum | awk '{print $1}')
  lock_dir="${TMPDIR:-/tmp}/philo-mise-dev-lock-$id"
  if [[ -d "$lock_dir" ]]; then
    old=""
    if [[ -f "$lock_dir/pid" ]]; then
      old=$(tr -d '[:space:]' <"$lock_dir/pid" || true)
    fi
    if [[ "$old" =~ ^[0-9]+$ ]] && kill -0 "$old" 2>/dev/null; then
      cmd=$(ps -p "$old" -o command= 2>/dev/null || true)
      if [[ "$cmd" == *scripts/dev.sh* ]]; then
        printf '%s\n' \
          "mise run dev is already running for this checkout." \
          "This task will not start a second copy, and it will not stop that run." >&2
        exit 1
      fi
    fi
    rm -rf "$lock_dir"
  fi
  mkdir "$lock_dir"
  printf '%s\n' "$$" >"$lock_dir/pid"
}

release_lock() {
  local old
  [[ -n "$lock_dir" && -d "$lock_dir" ]] || return 0
  old=""
  if [[ -f "$lock_dir/pid" ]]; then
    old=$(tr -d '[:space:]' <"$lock_dir/pid" || true)
  fi
  if [[ "$old" == "$$" ]]; then
    rm -rf "$lock_dir"
  fi
}

preflight() {
  local failed=0 rc
  if port_busy 5432; then
    printf '%s\n' \
      "Port 5432 on 127.0.0.1 is already in use." \
      "This task will not start another Postgres. It does not stop the existing process and it does not delete database volumes." \
      "Free the port, then run mise run dev again." >&2
    failed=1
  else
    rc=$?
    if [[ "$rc" -ne 1 ]]; then
      failed=1
    fi
  fi
  if port_busy 8080; then
    printf '%s\n' \
      "Port 8080 on 127.0.0.1 is already in use." \
      "This task will not start another server." \
      "Free the port, then run mise run dev again." >&2
    failed=1
  else
    rc=$?
    if [[ "$rc" -ne 1 ]]; then
      failed=1
    fi
  fi
  if ! check_docker; then
    failed=1
  fi
  if ! check_native; then
    failed=1
  fi
  [[ "$failed" -eq 0 ]]
}

port_busy() {
  local port="$1" rc=1
  if ! command -v perl >/dev/null 2>&1; then
    printf '%s\n' "perl is required to check whether port ${port} is already in use. Refusing to start a second copy." >&2
    return 2
  fi
  # A free port makes perl exit 1. Keep that status without tripping set -e.
  perl -MSocket -e '
    use Socket;
    my $port = shift;
    socket(my $s, PF_INET, SOCK_STREAM, getprotobyname("tcp")) or die $!;
    my $ok = bind($s, pack_sockaddr_in($port, inet_aton("127.0.0.1")));
    if (!$ok) {
      if ($!{EADDRINUSE}) { exit 0; }
      warn "could not bind 127.0.0.1:$port: $!\n";
      exit 2;
    }
    close $s;
    exit 1;
  ' "$port" && rc=0 || rc=$?
  return "$rc"
}

check_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    printf '%s\n' \
      "Docker is not available, so the local Postgres database cannot start." \
      "Install Docker Desktop on macOS, or Docker Engine with the Compose plugin on Linux, and start the Docker daemon." \
      "This task does not install Docker." >&2
    return 1
  fi
  if ! docker compose version >/dev/null 2>&1; then
    printf '%s\n' \
      "Docker Compose is not available." \
      "Install the Docker Compose plugin (docker compose) and run mise run dev again." \
      "This task does not install it." >&2
    return 1
  fi
  if ! docker info >/dev/null 2>&1; then
    printf '%s\n' \
      "Docker is installed, but the Docker daemon is not running." \
      "Start Docker and run mise run dev again." >&2
    return 1
  fi
  return 0
}

check_native() {
  local os
  os=$(uname -s)
  case "$os" in
    Darwin) check_macos ;;
    Linux) check_linux ;;
    *)
      printf '%s\n' \
        "The native desktop is built for macOS, Linux, and Windows." \
        "This task does not know which native libraries are missing on ${os}." >&2
      return 1
      ;;
  esac
}

check_macos() {
  local sdk fw ver major
  local missing
  missing=()
  if ! xcode-select -p >/dev/null 2>&1; then
    printf '%s\n' \
      "The macOS desktop needs Xcode Command Line Tools." \
      "They provide clang and the SDK frameworks AppKit, CoreFoundation, CoreGraphics, CoreText, Foundation, ImageIO, IOKit, and QuartzCore." \
      "Install them with: xcode-select --install" \
      "This task does not install them." >&2
    return 1
  fi
  if ! xcrun --find clang >/dev/null 2>&1; then
    printf '%s\n' \
      "clang is missing. The macOS desktop needs it to build." \
      "Install Xcode Command Line Tools with: xcode-select --install" \
      "This task does not install them." >&2
    return 1
  fi
  sdk=$(xcrun --sdk macosx --show-sdk-path 2>/dev/null || true)
  if [[ -z "$sdk" || ! -d "$sdk" ]]; then
    printf '%s\n' \
      "The macOS SDK is missing. The desktop links AppKit and the other system frameworks from that SDK." \
      "Install Xcode Command Line Tools with: xcode-select --install" \
      "This task does not install them." >&2
    return 1
  fi
  for fw in AppKit CoreFoundation CoreGraphics CoreText Foundation ImageIO IOKit QuartzCore; do
    if [[ ! -d "$sdk/System/Library/Frameworks/${fw}.framework" ]]; then
      missing+=("$fw")
    fi
  done
  if [[ "${#missing[@]}" -gt 0 ]]; then
    printf '%s\n' \
      "Missing macOS frameworks: ${missing[*]}." \
      "They ship with the macOS SDK. Reinstall Xcode Command Line Tools: xcode-select --install" \
      "This task does not install them." >&2
    return 1
  fi
  ver=$(sw_vers -productVersion 2>/dev/null || true)
  major=${ver%%.*}
  if [[ "$major" =~ ^[0-9]+$ && "$major" -lt 14 ]]; then
    printf '%s\n' "The desktop needs macOS 14 or newer. This Mac is ${ver}." >&2
    return 1
  fi
  return 0
}

check_linux() {
  local found cc_ok=0
  local missing
  missing=()
  if ! command -v pkg-config >/dev/null 2>&1; then
    missing+=("pkg-config")
  fi
  if ! pkg-config --exists 'gtk4 >= 4.14' 2>/dev/null; then
    found=$(pkg-config --modversion gtk4 2>/dev/null || true)
    if [[ -n "$found" ]]; then
      missing+=("GTK ${found} (need 4.14 or newer)")
    else
      missing+=("GTK 4.14 or newer (gtk4)")
    fi
  fi
  if ! pkg-config --exists pango 2>/dev/null; then
    missing+=("Pango")
  fi
  if ! pkg-config --exists pangocairo 2>/dev/null; then
    missing+=("PangoCairo")
  fi
  if command -v cc >/dev/null 2>&1 || command -v gcc >/dev/null 2>&1 || command -v clang >/dev/null 2>&1; then
    cc_ok=1
  fi
  if [[ "$cc_ok" -ne 1 ]]; then
    missing+=("a C compiler (cc, gcc, or clang)")
  fi
  if [[ "${#missing[@]}" -gt 0 ]]; then
    local item joined=""
    for item in "${missing[@]}"; do
      if [[ -n "$joined" ]]; then
        joined="$joined; "
      fi
      joined="$joined$item"
    done
    printf '%s\n' \
      "The Linux desktop cannot build yet. Missing: ${joined}." \
      "GTK 4.14 or newer and its development files are required. On Debian or Ubuntu: sudo apt-get install pkg-config libgtk-4-dev build-essential" \
      "On Fedora: sudo dnf install gtk4-devel pango-devel gcc pkgconf-pkg-config" \
      "This task does not install system packages." >&2
    return 1
  fi
  return 0
}

compose() {
  # Keep credentials out of Compose. Only the postgres service is started.
  env \
    -u PHILO_AUTH_CREDENTIALS \
    -u PHILO_ACTOR_TOKEN \
    -u PHILO_PROVIDER_API_KEY \
    -u PHILO_MAKEZ_API_TOKEN \
    -u PHILO_MAKEZ_API_URL \
    -u PHILO_MAKEZ_URL \
    -u PHILO_EXECUTION_FIXTURE \
    -u PHILO_MODEL_ADAPTER \
    -u PHILO_BUSINESS_FIXTURE \
    docker compose --project-directory "$root" -f "$root/docker-compose.yml" "$@"
}

start_postgres() {
  local help
  printf '%s\n' "Starting Postgres."
  postgres_started=1
  help=$(compose up --help 2>&1 || true)
  if [[ "$help" == *"--wait"* ]]; then
    compose up -d --wait postgres
  else
    compose up -d postgres
    wait_postgres_healthy
  fi
  if ! wait_tcp 5432 30; then
    printf '%s\n' \
      "Postgres did not accept connections on 127.0.0.1:5432." \
      "The database volume was not deleted." >&2
    exit 1
  fi
  printf '%s\n' "Postgres is ready."
}

wait_postgres_healthy() {
  local deadline cid status
  cid=$(compose ps -q postgres)
  if [[ -z "$cid" ]]; then
    printf '%s\n' "Postgres did not start. The database volume was not deleted." >&2
    exit 1
  fi
  deadline=$((SECONDS + 90))
  while [[ "$SECONDS" -lt "$deadline" ]]; do
    status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null || true)
    if [[ "$status" == "healthy" ]]; then
      return 0
    fi
    if [[ "$status" == "exited" || "$status" == "dead" ]]; then
      break
    fi
    sleep 1
  done
  printf '%s\n' "Postgres did not become ready. The database volume was not deleted." >&2
  exit 1
}

wait_tcp() {
  local port="$1" timeout="$2" deadline
  deadline=$((SECONDS + timeout))
  while [[ "$SECONDS" -lt "$deadline" ]]; do
    if perl -MIO::Socket::INET -e '
      my $port = shift;
      my $s = IO::Socket::INET->new(PeerAddr => "127.0.0.1", PeerPort => $port, Timeout => 1);
      exit($s ? 0 : 1);
    ' "$port"; then
      return 0
    fi
    sleep 0.5
  done
  return 1
}

resolve_auth() {
  local out="$run_dir/auth.env"
  set +e
  perl -e '
    use strict;
    use warnings;
    my $dest = $ARGV[0];
    my $saved = $ARGV[1];
    my $creds = $ENV{PHILO_AUTH_CREDENTIALS} // "";
    my $token = $ENV{PHILO_ACTOR_TOKEN} // "";
    $creds =~ s/\A\s+|\s+\z//g;
    $token =~ s/\A\s+|\s+\z//g;

    sub fail_format { exit 3 }
    sub fail_mismatch { exit 2 }
    sub fail_saved { exit 4 }

    sub parse {
      my ($raw) = @_;
      die "format\n" if !defined $raw || $raw eq "";
      my @out;
      foreach my $part (split /,/, $raw) {
        $part =~ s/\A\s+|\s+\z//g;
        next if $part eq "";
        my @fields = split /\|/, $part, -1;
        die "format\n" if @fields != 3;
        foreach my $field (@fields) {
          die "format\n" if $field eq "" || $field =~ /\s/;
        }
        push @out, [@fields];
      }
      die "format\n" if !@out;
      return @out;
    }

    sub write_env {
      my ($path, $c, $t) = @_;
      open my $fh, ">", $path or die $!;
      chmod 0600, $path;
      print $fh "PHILO_AUTH_CREDENTIALS=$c\n";
      print $fh "PHILO_ACTOR_TOKEN=$t\n";
      close $fh or die $!;
    }

    my $how;
    if ($creds ne "" || $token ne "") {
      $how = "env";
      my @entries;
      if ($creds ne "" && $token ne "") {
        @entries = eval { parse($creds) };
        fail_format() if $@;
        my $found = 0;
        foreach my $entry (@entries) {
          $found = 1 if $entry->[0] eq $token;
        }
        fail_mismatch() unless $found;
      } elsif ($creds ne "") {
        @entries = eval { parse($creds) };
        fail_format() if $@;
        $token = $entries[0][0];
      } else {
        fail_format() if $token =~ /[\s|,]/;
        $creds = "$token|local-actor|local-owner";
        eval { parse($creds) };
        fail_format() if $@;
      }
    } else {
      $how = "file";
      if (-f $saved) {
        open my $fh, "<", $saved or fail_saved();
        my %got;
        while (my $line = <$fh>) {
          next if $line =~ /\A\s*#/ || $line =~ /\A\s*\z/;
          chomp $line;
          if ($line =~ /\APHILO_AUTH_CREDENTIALS=(.*)\z/) { $got{creds} = $1 }
          elsif ($line =~ /\APHILO_ACTOR_TOKEN=(.*)\z/) { $got{token} = $1 }
        }
        close $fh;
        fail_saved() if !defined $got{creds} || !defined $got{token};
        my @entries = eval { parse($got{creds}) };
        fail_saved() if $@ || !@entries;
        my $found = 0;
        foreach my $entry (@entries) {
          $found = 1 if $entry->[0] eq $got{token};
        }
        fail_saved() unless $found;
        $creds = $got{creds};
        $token = $got{token};
      } else {
        $how = "new";
        open my $rnd, "<", "/dev/urandom" or die $!;
        read $rnd, my $buf, 16 or die $!;
        close $rnd;
        $token = unpack("H*", $buf);
        $creds = "$token|local-actor|local-owner";
        if (-l $saved) { exit 5 }
        my $tmp = "$saved.tmp.$$";
        write_env($tmp, $creds, $token);
        rename $tmp, $saved or die $!;
        chmod 0600, $saved;
      }
    }
    write_env($dest, $creds, $token);
    open my $howf, ">", "$dest.how" or die $!;
    print $howf "$how\n";
    close $howf;
    exit 0;
  ' "$out" "$root/.env.local"
  local rc=$?
  set -e
  if [[ "$rc" -eq 2 ]]; then
    printf '%s\n' \
      "PHILO_ACTOR_TOKEN does not match a token in PHILO_AUTH_CREDENTIALS." \
      "Set both from the same credential, or unset one of them." \
      "The values were not printed." >&2
    exit 1
  fi
  if [[ "$rc" -eq 3 ]]; then
    printf '%s\n' \
      "PHILO_AUTH_CREDENTIALS must be comma-separated token|actor|owner entries, with no spaces." \
      "The value was not printed." >&2
    exit 1
  fi
  if [[ "$rc" -eq 4 ]]; then
    printf '%s\n' \
      "The saved local credential in .env.local is not usable." \
      "It needs PHILO_AUTH_CREDENTIALS=token|actor|owner and a PHILO_ACTOR_TOKEN that matches one of those tokens." \
      "This task did not print the file." >&2
    exit 1
  fi
  if [[ "$rc" -eq 5 ]]; then
    printf '%s\n' ".env.local is a symlink. Refusing to write a credential there." >&2
    exit 1
  fi
  if [[ "$rc" -ne 0 ]]; then
    printf '%s\n' "Could not prepare the local credential. It was not printed." >&2
    exit 1
  fi
  local how
  how=$(cat "$out.how")
  case "$how" in
    env) printf '%s\n' "Using one credential from the environment for the server and the desktop." ;;
    file) printf '%s\n' "Using the saved local credential for the server and the desktop. It is not printed." ;;
    new) printf '%s\n' "Using a generated local credential for the server and the desktop. It is not printed." ;;
    *) printf '%s\n' "Could not prepare the local credential. It was not printed." >&2; exit 1 ;;
  esac
}

read_auth() {
  local line
  AUTH_CREDENTIALS=""
  AUTH_TOKEN=""
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in
      PHILO_AUTH_CREDENTIALS=*) AUTH_CREDENTIALS="${line#PHILO_AUTH_CREDENTIALS=}" ;;
      PHILO_ACTOR_TOKEN=*) AUTH_TOKEN="${line#PHILO_ACTOR_TOKEN=}" ;;
    esac
  done <"$run_dir/auth.env"
  if [[ -z "$AUTH_CREDENTIALS" || -z "$AUTH_TOKEN" ]]; then
    printf '%s\n' "Could not prepare the local credential. It was not printed." >&2
    exit 1
  fi
  local entry tok ok=0
  local entries
  IFS=',' read -r -a entries <<<"$AUTH_CREDENTIALS"
  for entry in "${entries[@]}"; do
    tok="${entry%%|*}"
    if [[ "$tok" == "$AUTH_TOKEN" ]]; then
      ok=1
    fi
  done
  if [[ "$ok" -ne 1 ]]; then
    printf '%s\n' "The server credential and the desktop token do not match. They were not printed." >&2
    exit 1
  fi
}

fixture_vars() {
  printf '%s\n' \
    PHILO_EXECUTION_FIXTURE \
    PHILO_MODEL_ADAPTER \
    PHILO_BUSINESS_FIXTURE \
    PHILO_PROVIDER_API_KEY \
    PHILO_PROVIDER_BASE_URL \
    PHILO_MAKEZ_URL \
    PHILO_MAKEZ_API_URL \
    PHILO_MAKEZ_API_TOKEN \
    PHILO_MAKEZ_WORKSPACE_ID \
    PHILO_MAKEZ_BINDING \
    PHILO_DEV_CHUNK_DELAY \
    PHILO_TEST_EDIT_SUMMARY \
    PHILO_RESTART_URL \
    PHILO_FIXTURE_SUFFIX \
    PHILO_PUBLISH_CRASH
}

write_server_script() {
  local var
  read_auth
  {
    printf '%s\n' '#!/bin/bash' 'set +x' 'set -euo pipefail'
    while IFS= read -r var; do
      printf 'unset %s\n' "$var"
    done < <(fixture_vars)
    printf 'unset %s\n' PHILO_ACTOR_TOKEN
    printf 'export PHILO_DATABASE_URL=%q\n' 'postgres://philo:philo@127.0.0.1:5432/philo?sslmode=disable'
    printf 'export PHILO_HTTP_ADDR=%q\n' '127.0.0.1:8080'
    printf 'export PHILO_AUTH_CREDENTIALS=%q\n' "$AUTH_CREDENTIALS"
    printf 'exec go -C %q run .\n' "$root/server"
  } >"$run_dir/server.sh"
  chmod 700 "$run_dir/server.sh"
}

write_desktop_script() {
  local var
  read_auth
  {
    printf '%s\n' '#!/bin/bash' 'set +x' 'set -euo pipefail'
    while IFS= read -r var; do
      printf 'unset %s\n' "$var"
    done < <(fixture_vars)
    printf 'unset %s\n' PHILO_DATABASE_URL PHILO_AUTH_CREDENTIALS
    printf 'export PHILO_ACTOR_TOKEN=%q\n' "$AUTH_TOKEN"
    printf 'cd %q\n' "$root/desktop"
    printf 'exec cargo run --manifest-path %q\n' "$root/desktop/Cargo.toml"
  } >"$run_dir/desktop.sh"
  chmod 700 "$run_dir/desktop.sh"
}

start_server() {
  write_server_script
  # Drop the credential from this shell. The server script has its own copy.
  AUTH_CREDENTIALS=""
  AUTH_TOKEN=""
  printf '%s\n' "Starting the Go server."
  if command -v setsid >/dev/null 2>&1; then
    setsid "$run_dir/server.sh" &
    server_pid=$!
  elif command -v perl >/dev/null 2>&1; then
    perl -MPOSIX=setsid -e 'setsid() or die "setsid: $!"; exec @ARGV or die "exec: $!"' -- "$run_dir/server.sh" &
    server_pid=$!
  else
    printf '%s\n' "Need setsid or perl to start the server in its own session." >&2
    exit 1
  fi
}

wait_for_server() {
  local deadline
  deadline=$((SECONDS + 300))
  while [[ "$SECONDS" -lt "$deadline" ]]; do
    if ! kill -0 "$server_pid" 2>/dev/null; then
      printf '%s\n' "The Go server exited before it accepted requests." >&2
      exit 1
    fi
    if health_ok; then
      printf '%s\n' "The server is ready."
      return 0
    fi
    sleep 0.5
  done
  printf '%s\n' "The Go server did not become ready at http://127.0.0.1:8080/v1/health." >&2
  exit 1
}

health_ok() {
  perl -MIO::Socket::INET -e '
    my $s = IO::Socket::INET->new(
      PeerAddr => "127.0.0.1",
      PeerPort => 8080,
      Timeout  => 2,
    );
    exit 1 unless $s;
    $s->autoflush(1);
    print $s "GET /v1/health HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n";
    my $line = <$s>;
    exit 1 unless defined $line;
    exit($line =~ m{\AHTTP/\d+(?:\.\d+)? 200\b} ? 0 : 1);
  '
}

start_desktop() {
  write_desktop_script
  AUTH_CREDENTIALS=""
  AUTH_TOKEN=""
  printf '%s\n' "Starting the desktop."
  bash "$run_dir/desktop.sh" &
  desktop_pid=$!
  (
    while kill -0 "$server_pid" 2>/dev/null; do
      sleep 1
    done
    stop_desktop || true
  ) &
  watcher_pid=$!
  set +e
  wait "$desktop_pid"
  local status=$?
  set -e
  exit "$status"
}

command_matches() {
  local cmd="$1"
  shift
  local needle
  for needle in "$@"; do
    if [[ "$cmd" == *"$needle"* ]]; then
      return 0
    fi
  done
  return 1
}

stop_server() {
  local pid="${server_pid:-}" cmd pgid
  [[ "$pid" =~ ^[0-9]+$ ]] || return 0
  if ! kill -0 "$pid" 2>/dev/null; then
    return 0
  fi
  cmd=$(ps -ww -p "$pid" -o command= 2>/dev/null || true)
  if ! command_matches "$cmd" "$run_dir/server.sh" "go -C $root/server run"; then
    return 0
  fi
  pgid=$(ps -ww -p "$pid" -o pgid= 2>/dev/null | tr -d '[:space:]' || true)
  if [[ "$pgid" == "$pid" ]]; then
    kill -TERM -"$pid" 2>/dev/null || true
  else
    kill_matching_tree "$pid" "$run_dir/server.sh" "go -C $root/server run"
  fi
  local i=0
  while [[ "$i" -lt 50 ]]; do
    if ! kill -0 "$pid" 2>/dev/null; then
      return 0
    fi
    sleep 0.1
    i=$((i + 1))
  done
  cmd=$(ps -ww -p "$pid" -o command= 2>/dev/null || true)
  pgid=$(ps -ww -p "$pid" -o pgid= 2>/dev/null | tr -d '[:space:]' || true)
  if command_matches "$cmd" "$run_dir/server.sh" "go -C $root/server run"; then
    if [[ "$pgid" == "$pid" ]]; then
      kill -KILL -"$pid" 2>/dev/null || true
    else
      kill -KILL "$pid" 2>/dev/null || true
    fi
  fi
}

stop_desktop() {
  local pid="${desktop_pid:-}"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 0
  kill_matching_tree "$pid" "$run_dir/desktop.sh" "cargo run --manifest-path $root/desktop/Cargo.toml" "$root/desktop/target"
}

stop_watcher() {
  local pid="${watcher_pid:-}" ppid
  [[ "$pid" =~ ^[0-9]+$ ]] || return 0
  if ! kill -0 "$pid" 2>/dev/null; then
    return 0
  fi
  ppid=$(ps -ww -p "$pid" -o ppid= 2>/dev/null | tr -d '[:space:]' || true)
  if [[ "$ppid" == "$$" ]]; then
    kill -TERM "$pid" 2>/dev/null || true
  fi
}

kill_matching_tree() {
  local root_pid="$1"
  shift
  local cmd
  [[ "$root_pid" =~ ^[0-9]+$ ]] || return 0
  if ! kill -0 "$root_pid" 2>/dev/null; then
    return 0
  fi
  cmd=$(ps -ww -p "$root_pid" -o command= 2>/dev/null || true)
  if ! command_matches "$cmd" "$@"; then
    return 0
  fi
  # Children first. Only PIDs reached by walking from the process we started.
  signal_tree "$root_pid" TERM
  local i=0
  while [[ "$i" -lt 50 ]]; do
    if ! kill -0 "$root_pid" 2>/dev/null; then
      return 0
    fi
    sleep 0.1
    i=$((i + 1))
  done
  cmd=$(ps -ww -p "$root_pid" -o command= 2>/dev/null || true)
  if command_matches "$cmd" "$@"; then
    signal_tree "$root_pid" KILL
  fi
}

signal_tree() {
  local root_pid="$1" sig="$2" child
  while IFS= read -r child; do
    [[ "$child" == "$root_pid" ]] && continue
    [[ "$child" =~ ^[0-9]+$ ]] || continue
    kill -"$sig" "$child" 2>/dev/null || true
  done < <(descendants "$root_pid")
  kill -"$sig" "$root_pid" 2>/dev/null || true
}

descendants() {
  local pid="$1" kids kid
  kids=$(ps -ax -o pid=,ppid= | awk -v p="$pid" '$2 == p { print $1 }')
  for kid in $kids; do
    descendants "$kid"
  done
  printf '%s\n' "$pid"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi

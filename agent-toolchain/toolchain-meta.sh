#!/usr/bin/env bash
# Shared per-language metadata for the on-demand toolchain index/publisher.
# Sourced (never executed) by build-index.sh and publish-artifacts.sh so the
# tool list lives in ONE place (no drift between generate + publish).
#
# Contract: abc-protocol/deploy/DEVELOP.md -> "Toolchain index".
#
#   PUBLISHED_LANGS            the installable set (phase 1: pure-unpack only)
#   toolchain_version <lang>   pinned version (the index key)
#   toolchain_requires <lang>  comma-separated toolchain deps ("" if none)
#   toolchain_kind <lang>      as-is | build
#       as-is  = relocatable prebuilt: upload the upstream cache artifact
#                verbatim; the installer unpacks it (strip/bin/rename).
#       build  = needs install.sh / a compile (PHASE 2): the publisher builds
#                once with the final prefix and ships the result.
#   toolchain_specs <lang>     one line per UPSTREAM cache artifact (as-is):
#                                <URLVAR>|<format>|<strip>|<bin>[|<rename>[|<os>|<arch>]]
#       bin     relative to the version root ("{root}" allowed)
#       rename  "src->dst" (both relative to the version root, applied after
#               strip), e.g. dart: "dart-sdk->dart"
#       os/arch restrict the artifact to a platform (empty = any) — a version
#               may list one artifact per platform; the installer picks the
#               ones matching its own GOOS/GOARCH.
#   toolchain_install <lang>   space-separated argv run after unpacking
#                              ("{root}" expands to the version dir), e.g.
#                              rust: "./install.sh --prefix={root} …". Empty = none.
#   toolchain_env <lang>       semicolon-separated NAME=VALUE runtime env
#                              ("{root}" expands), e.g. ruby's LD_LIBRARY_PATH.
#   toolchain_unpack_dir <lang> subdir under the version root to unpack into and
#                              run install[] from (default "" = the root itself).
#                              Needed when the installer refuses its own dir
#                              (rust/clojure install.sh).
#   toolchain_install_prefix <lang> subdir under the version root that install[]
#                              and `bin` resolve against (the installer's OUTPUT
#                              dir; default "" = the version root). E.g. conda.
#
# strip/bin/rename/install/env/unpack_dir mirror each Dockerfile.<lang>'s unpack
# (validate against it before trusting a new entry).

# ---- published (installable) set --------------------------------------------
# Phase 1 = "download -> verify -> unpack -> PATH", no install.sh, no compile.
# Phase 2 = the rest, still "download -> verify -> unpack (+ optional install
# step / runtime env)". Deferred to phase 2b (not published): lua + r (compile
# from source), elixir + gleam (multi-part OTP build + hex), clang
# (apt.llvm.org + conan wheels), java + swift (very large images), conda (its
# installer refuses a non-empty prefix), clojure (its install.sh is sed/ruby
# based), perl (cpanm's `#!perl` shebang needs the Dockerfile's make-install).
PUBLISHED_LANGS="${PUBLISHED_LANGS:-go node python java25 dotnet php dart kotlin zig bun pixi scala groovy deno julia crystal ocaml haskell ruby rust flutter java swift gleam godot erlang elixir conda clojure perl lua r clang cmake ninja}"

# ---- versions (the index key; kept explicit — upstream naming varies) --------
toolchain_version() {
  case "$1" in
    go) echo "1.27.1" ;; node) echo "26.9.0" ;; python) echo "3.14.7" ;;
    rust) echo "1.98.1" ;; java) echo "26.0.2.1" ;; java25) echo "25.0.4.1" ;;
    kotlin) echo "2.4.20" ;; scala) echo "1.17.1" ;; groovy) echo "4.0.33" ;;
    clojure) echo "1.12.6.1673" ;; dart) echo "3.13.4" ;; dotnet) echo "10.0.401" ;;
    elixir) echo "1.20.4" ;; gleam) echo "1.18.1" ;; php) echo "8.5.8" ;;
    erlang) echo "29.1" ;;
    ruby) echo "4.0.7" ;; swift) echo "6.4.0" ;; zig) echo "0.16.0" ;;
    bun) echo "1.4.2" ;; deno) echo "2.9.7" ;; julia) echo "1.13.0" ;;
    crystal) echo "1.21.0" ;; ocaml) echo "2.6.0" ;; haskell) echo "0.2.6.2" ;;
    lua) echo "5.5.1" ;; perl) echo "1.7049" ;; r) echo "4.6.1" ;;
    flutter) echo "3.47.6" ;;
    clang) echo "23.1.2" ;; cmake) echo "4.4.3" ;; ninja) echo "1.13.2" ;;
    conda) echo "latest" ;; pixi) echo "latest" ;; godot) echo "4.7.2" ;;
    *) echo "unknown" ;;
  esac
}

# ---- toolchain deps (index `requires`) --------------------------------------
toolchain_requires() {
  case "$1" in
    kotlin|groovy|clojure|scala) echo "java25" ;;
    elixir|gleam) echo "erlang" ;;
    *) echo "" ;;
  esac
}

# ---- publish kind -----------------------------------------------------------
toolchain_kind() {
  case "$1" in
    # Only source compiles need a publisher-side build (phase 2b); everything
    # else is as-is: unpack (+ optional `install[]` step, e.g. rust/elixir/
    # clojure/conda) and/or a runtime `env[]` (ruby).
    lua|r) echo "build" ;;
    *) echo "as-is" ;;
  esac
}

# ---- upstream artifact specs (as-is) ----------------------------------------
# <URLVAR>|<format>|<strip>|<bin>[|<rename>]
toolchain_specs() {
  case "$1" in
    # ---- phase 1 (published now) ----
    go)      echo "GO_URL|tar.gz|1|bin" ;;
    node)    echo "NODE_URL|tar.xz|1|bin" ;;
    python)  echo "PYTHON_URL|tar.gz|1|bin" ;;               # install_only archive has a top `python/` dir
    java25)  echo "JDK25_URL|tar.gz|1|bin" ;;
    dotnet)  echo "DOTNET_URL|tar.gz|0|." ;;                 # dotnet at root (no strip)
    php)     echo "PHP_URL|tar.gz|1|bin;COMPOSER_URL|phar|0|bin/composer" ;;
    dart)    echo "DART_URL|zip|0|dart/bin|dart-sdk->dart" ;;  # keep top dir, rename dart-sdk -> dart
    kotlin)  echo "KOTLIN_URL|zip|1|bin" ;;
    zig)     echo "ZIG_URL|tar.xz|1|." ;;                    # zig at root
    bun)     echo "BUN_URL|zip|1|." ;;                       # bun at root
    pixi)    echo "PIXI_URL|raw|0|bin/pixi" ;;
    # ---- phase 2 (pure unpack: as-is, + optional install[]/env[]) ----
    java)    echo "JDK_URL|tar.gz|1|bin;GRADLE_URL|zip|1|bin" ;;
    scala)   echo "SCALA_CLI_URL|gz|0|bin/scala-cli;SBT_URL|tar.gz|1|bin" ;;
    groovy)  echo "GROOVY_URL|zip|1|bin" ;;
    # gleam ships a single musl binary at the archive ROOT (strip 0).
    gleam)   echo "GLEAM_URL|tar.gz|0|." ;;
    ruby)    echo "RUBY_URL|tar.gz|0|x64/bin" ;;             # ruby-builder layout: x64/{bin,lib}; + env
    flutter) echo "FLUTTER_LINUX_URL|tar.xz|1|bin||linux|amd64;FLUTTER_WINDOWS_URL|zip|1|bin||windows|amd64;FLUTTER_MACOS_URL|zip|1|bin||darwin|amd64;FLUTTER_MACOS_ARM64_URL|zip|1|bin||darwin|arm64" ;;
    swift)   echo "SWIFT_URL|tar.gz|1|usr/bin" ;;
    deno)    echo "DENO_URL|zip|0|." ;;                     # zip root holds `deno`
    julia)   echo "JULIA_URL|tar.gz|1|bin" ;;
    crystal) echo "CRYSTAL_URL|tar.gz|1|bin" ;;
    ocaml)   echo "OPAM_URL|raw|0|bin/opam" ;;
    haskell) echo "GHCUP_URL|raw|0|bin/ghcup" ;;
    perl)    echo "CPANM_URL|tar.gz|1|bin" ;;
    # clang/LLVM official prebuilt: tar.xz, root LLVM-<ver>-Linux-X64/ -> strip1 -> bin/.
    clang)   echo "LLVM_URL|tar.xz|1|bin" ;;
    cmake)   echo "CMAKE_URL|tar.gz|1|bin" ;;
    ninja)   echo "NINJA_URL|zip|0|." ;;                     # zip root holds `ninja` (single file)
    # godot ships one executable in the zip ROOT (strip 0); the zip has no exec
    # bit, so install[] chmods it. `bin "."` puts the version root on PATH.
    godot)   echo "GODOT_URL|zip|0|." ;;
    # rust/elixir/clojure/conda are as-is too, but need an install[] step (below).
    rust)    echo "RUST_URL|tar.gz|1|bin" ;;                 # install.sh --prefix={root} populates bin/
    # erlang (OTP) and elixir are SEPARATE toolchains: erlang is its own
    # relocatable build (its ./Install populates bin/), elixir requires it.
    erlang)  echo "OTP_URL|tar.gz|1|bin" ;;                  # ./Install writes {root}/bin (install[])
    elixir)  echo "ELIXIR_URL|zip|0|bin" ;;                  # zip root is bin/ (no top dir) -> strip 0
    clojure) echo "CLOJURE_URL|tar.gz|1|bin" ;;
    # conda: raw installer script placed at {root}/bin/miniconda.sh; it installs
    # into the fresh {root}/miniconda (install_prefix), whose bin/ joins PATH.
    conda)   echo "CONDA_URL|raw|0|bin/miniconda.sh" ;;
    # phase 2b (source compile): publisher ships ONE relocatable tarball.
    lua|r)   echo "" ;;
    *) return 1 ;;
  esac
}

# ---- post-unpack install step (as-is kinds) ---------------------------------
# Space-separated argv; {root} expands to the version dir. Runs with cwd=root.
toolchain_install() {
  case "$1" in
    rust)    echo "./install.sh --prefix={root} --without=rust-docs --disable-ldconfig" ;;
    # erlang: OTP's ./Install writes a relocatable tree into {root}.
    erlang)  echo "./Install -minimal {root}" ;;
    # clojure: the upstream install.sh is sed/ruby-based; do what Dockerfile.clojure
    # does inline (copy jars to libexec, write a java-launching shim).
    # The shim must NOT bake the install-time path: install[] runs in the .tmp
    # dir, which is then atomically renamed — a hardcoded path would go stale.
    # Use a RELATIVE classpath (java's `dir/*` expands to every jar) so it stays
    # valid after the rename.
    clojure) echo "mkdir -p {root}/libexec {root}/bin && cp ./*.jar {root}/libexec/ && cp deps.edn example-deps.edn tools.edn {root}/ && printf '#!/bin/sh\nexec java -cp \"\$(dirname \"\$0\")/../libexec/*\" clojure.main \"\$@\"\n' > {root}/bin/clojure && chmod 755 {root}/bin/clojure" ;;
    # perl: App::cpanminus, built+installed to the prefix (INSTALL_BASE).
    perl)    echo "perl Makefile.PL INSTALL_BASE={root} && make && make install" ;;
    # cwd = version root; the installer REFUSES an existing prefix, so it
    # installs into the fresh ./miniconda (install_prefix).
    conda)   echo "bash bin/miniconda.sh -b -p miniconda" ;;
    godot)   echo "chmod 755 {root}/Godot_v4.7.2-stable_linux.x86_64" ;;
    ninja)   echo "chmod 755 {root}/ninja" ;;                # zip has no exec bit
    *) echo "" ;;
  esac
}

# ---- runtime env (as-is kinds) ----------------------------------------------
# Semicolon-separated NAME=VALUE; {root} expands. Applied to the worker env
# (jobs inherit it) after a successful install.
toolchain_env() {
  case "$1" in
    ruby) echo "LD_LIBRARY_PATH={root}/x64/lib" ;;
    # perl: INSTALL_BASE puts App::cpanminus under {root}/lib/perl5; put it on @INC.
    perl) echo "PERL5LIB={root}/lib/perl5" ;;
    # crystal derives its worker pool from the host CPU inventory, which
    # overflows inside a container on a very large node (see Dockerfile.crystal).
    crystal) echo "CRYSTAL_WORKERS=4" ;;
    # clang bundles libc++ (libc++.so.1) but the binaries carry no rpath, so a
    # libc++-linked program needs the bundled lib dir on the loader path.
    clang) echo "LD_LIBRARY_PATH={root}/lib/x86_64-unknown-linux-gnu" ;;
    # JDK: JAVA_HOME (Dockerfile.java sets it); the `java`/`javac` bin is on PATH.
    java) echo "JAVA_HOME={root}" ;;
    # .NET SDK: DOTNET_ROOT (Dockerfile.dotnet sets it).
    dotnet) echo "DOTNET_ROOT={root}" ;;
    *) echo "" ;;
  esac
}

# ---- unpack subdir (install.sh refuses its own directory) -------------------
toolchain_unpack_dir() {
  case "$1" in
    rust|clojure|perl) echo "dist" ;;
    *) echo "" ;;
  esac
}

# ---- install prefix (installer's OUTPUT dir) --------------------------------
toolchain_install_prefix() {
  case "$1" in
    conda) echo "miniconda" ;;
    *) echo "" ;;
  esac
}

# ---- build-time system deps (apt) -------------------------------------------
# Space-separated apt packages a language needs at BUILD (publish) time; the
# publisher apt-installs them in its build container. Toolchains whose upstream
# is relocatable ignore this. swift's tarball needs these shared libs.
toolchain_deps() {
  case "$1" in
    swift) echo "libc6-dev binutils libcurl4t64 libedit2 libncurses6 libsqlite3-0 libxml2 libz3-4 tzdata zlib1g-dev libpython3-dev libstdc++-14-dev" ;;
    *) echo "" ;;
  esac
}

# bin dir for a BUILT relocatable (relative to the version root).
toolchain_build_bin() { echo "bin"; }

# ---- cache filename for an upstream URL var (mirrors fetch-artifacts.sh) -----
toolchain_cache_name() {
  case "$1" in
    HEXKEY_URL) echo "registry-public-key.pem" ;;
    *) local u="${!1%%\?*}"; u="${u##*/}"; echo "${u//%2B/+}" ;;
  esac
}

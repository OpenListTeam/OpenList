set -e
appName="openlist"
builtAt="$(date +'%F %T %z')"
gitAuthor="The OpenList Projects Contributors <noreply@openlist.team>"
gitCommit=$(git log --pretty=format:"%h" -1)

# Set frontend repository, default to OpenListTeam/OpenList-Frontend
frontendRepo="${FRONTEND_REPO:-OpenListTeam/OpenList-Frontend}"

githubAuthArgs=""
if [ -n "$GITHUB_TOKEN" ]; then
  githubAuthArgs="--header \"Authorization: Bearer $GITHUB_TOKEN\""
fi

# Check for lite parameter
useLite=false
if [[ "$*" == *"lite"* ]]; then
  useLite=true
fi

if [ "$1" = "dev" ]; then
  version="dev"
  webVersion="rolling"
elif [ "$1" = "beta" ]; then
  version="beta"
  webVersion="rolling"
else
  git tag -d beta || true
  # Always true if there's no tag
  version=$(git describe --abbrev=0 --tags 2>/dev/null || echo "v0.0.0")
  webVersion=$(eval "curl -fsSL --max-time 2 $githubAuthArgs \"https://api.github.com/repos/$frontendRepo/releases/latest\"" | grep "tag_name" | head -n 1 | awk -F ":" '{print $2}' | sed 's/\"//g;s/,//g;s/ //g')
fi

echo "backend version: $version"
echo "frontend version: $webVersion"
if [ "$useLite" = true ]; then
  echo "using lite frontend"
else
  echo "using standard frontend"
fi

ldflags="\
-w -s \
-X 'github.com/OpenListTeam/OpenList/v4/internal/conf.BuiltAt=$builtAt' \
-X 'github.com/OpenListTeam/OpenList/v4/internal/conf.GitAuthor=$gitAuthor' \
-X 'github.com/OpenListTeam/OpenList/v4/internal/conf.GitCommit=$gitCommit' \
-X 'github.com/OpenListTeam/OpenList/v4/internal/conf.Version=$version' \
-X 'github.com/OpenListTeam/OpenList/v4/internal/conf.WebVersion=$webVersion' \
"

FetchWebRolling() {
  pre_release_json=$(eval "curl -fsSL --max-time 2 $githubAuthArgs -H \"Accept: application/vnd.github.v3+json\" \"https://api.github.com/repos/$frontendRepo/releases/tags/rolling\"")
  pre_release_assets=$(echo "$pre_release_json" | jq -r '.assets[].browser_download_url')
  
  # There is no lite for rolling
  pre_release_tar_url=$(echo "$pre_release_assets" | grep "openlist-frontend-dist" | grep -v "lite" | grep "\.tar\.gz$")

  curl -fsSL "$pre_release_tar_url" -o dist.tar.gz
  rm -rf public/dist && mkdir -p public/dist
  tar -zxvf dist.tar.gz -C public/dist
  rm -rf dist.tar.gz
}

FetchWebRelease() {
  release_json=$(eval "curl -fsSL --max-time 2 $githubAuthArgs -H \"Accept: application/vnd.github.v3+json\" \"https://api.github.com/repos/$frontendRepo/releases/latest\"")
  release_assets=$(echo "$release_json" | jq -r '.assets[].browser_download_url')
  
  if [ "$useLite" = true ]; then
    release_tar_url=$(echo "$release_assets" | grep "openlist-frontend-dist-lite" | grep "\.tar\.gz$")
  else
    release_tar_url=$(echo "$release_assets" | grep "openlist-frontend-dist" | grep -v "lite" | grep "\.tar\.gz$")
  fi
  
  curl -fsSL "$release_tar_url" -o dist.tar.gz
  rm -rf public/dist && mkdir -p public/dist
  tar -zxvf dist.tar.gz -C public/dist
  rm -rf dist.tar.gz
}

BuildWinArm64() {
  echo building for windows-arm64
  chmod +x ./wrapper/zcc-arm64
  chmod +x ./wrapper/zcxx-arm64
  export GOOS=windows
  export GOARCH=arm64
  export CC=$(pwd)/wrapper/zcc-arm64
  export CXX=$(pwd)/wrapper/zcxx-arm64
  export CGO_ENABLED=1
  go build -o "$1" -ldflags="$ldflags" -tags=jsoniter .
}

BuildWin7() {
  # Setup Win7 Go compiler (patched version that supports Windows 7)
  go_version=$(go version | grep -o 'go[0-9]\+\.[0-9]\+\.[0-9]\+' | sed 's/go//')
  echo "Detected Go version: $go_version"
  
  curl -fsSL --retry 3 -o go-win7.zip -H "Authorization: Bearer $GITHUB_TOKEN" \
    "https://github.com/XTLS/go-win7/releases/download/patched-${go_version}/go-for-win7-linux-amd64.zip"
  
  rm -rf go-win7
  unzip go-win7.zip -d go-win7
  rm go-win7.zip
  
  # Set permissions for all wrapper files
  chmod +x ./wrapper/zcc-win7
  chmod +x ./wrapper/zcxx-win7
  chmod +x ./wrapper/zcc-win7-386
  chmod +x ./wrapper/zcxx-win7-386
  
  # Build for both 386 and amd64 architectures
  for arch in "386" "amd64"; do
    echo "building for windows7-${arch}"
    export GOOS=windows
    export GOARCH=${arch}
    export CGO_ENABLED=1
    
    # Use architecture-specific wrapper files
    if [ "$arch" = "386" ]; then
      export CC=$(pwd)/wrapper/zcc-win7-386
      export CXX=$(pwd)/wrapper/zcxx-win7-386
    else
      export CC=$(pwd)/wrapper/zcc-win7
      export CXX=$(pwd)/wrapper/zcxx-win7
    fi
    
    # Use the patched Go compiler for Win7 compatibility
    $(pwd)/go-win7/bin/go build -o "${1}-${arch}.exe" -ldflags="$ldflags" -tags=jsoniter .
  done
}

BuildDev() {
  rm -rf .git/
  mkdir -p "dist"
  muslflags="--extldflags '-static -fpic' $ldflags"
  BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/"
  FILES=(x86_64-linux-musl-cross aarch64-linux-musl-cross)
  for i in "${FILES[@]}"; do
    url="${BASE}${i}.tgz"
    curl -fsSL -o "${i}.tgz" "${url}"
    sudo tar xf "${i}.tgz" --strip-components 1 -C /usr/local
  done
  OS_ARCHES=(linux-musl-amd64 linux-musl-arm64)
  CGO_ARGS=(x86_64-linux-musl-gcc aarch64-linux-musl-gcc)
  for i in "${!OS_ARCHES[@]}"; do
    os_arch=${OS_ARCHES[$i]}
    cgo_cc=${CGO_ARGS[$i]}
    echo building for ${os_arch}
    export GOOS=${os_arch%%-*}
    export GOARCH=${os_arch##*-}
    export CC=${cgo_cc}
    export CGO_ENABLED=1
    go build -o ./dist/$appName-$os_arch -ldflags="$muslflags" -tags=jsoniter .
  done
  xgo -targets=windows/amd64,darwin/amd64,darwin/arm64 -out "$appName" -ldflags="$ldflags" -tags=jsoniter .
  mv "$appName"-* dist
  cd dist
  # cp ./"$appName"-windows-amd64.exe ./"$appName"-windows-amd64-upx.exe
  # upx -9 ./"$appName"-windows-amd64-upx.exe
  find . -type f -print0 | xargs -0 md5sum >md5.txt
  cat md5.txt
}

BuildDocker() {
  go build -o ./bin/"$appName" -ldflags="$ldflags" -tags=jsoniter .
}

PrepareBuildDockerMusl() {
  mkdir -p build/musl-libs
  BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/"
  FILES=(x86_64-linux-musl-cross aarch64-linux-musl-cross i486-linux-musl-cross armv6-linux-musleabihf-cross armv7l-linux-musleabihf-cross riscv64-linux-musl-cross powerpc64le-linux-musl-cross loongarch64-linux-musl-cross) ## Disable s390x-linux-musl-cross builds
  for i in "${FILES[@]}"; do
    url="${BASE}${i}.tgz"
    lib_tgz="build/${i}.tgz"
    curl -fsSL -o "${lib_tgz}" "${url}"
    tar xf "${lib_tgz}" --strip-components 1 -C build/musl-libs
    rm -f "${lib_tgz}"
  done
}

BuildDockerMultiplatform() {
  go mod download

  # run PrepareBuildDockerMusl before build
  export PATH=$PATH:$PWD/build/musl-libs/bin

  docker_lflags="--extldflags '-static -fpic' $ldflags"
  export CGO_ENABLED=1

  OS_ARCHES=(linux-amd64 linux-arm64 linux-386 linux-riscv64 linux-ppc64le linux-loong64) ## Disable linux-s390x builds
  CGO_ARGS=(x86_64-linux-musl-gcc aarch64-linux-musl-gcc i486-linux-musl-gcc riscv64-linux-musl-gcc powerpc64le-linux-musl-gcc loongarch64-linux-musl-gcc) ## Disable s390x-linux-musl-gcc builds
  for i in "${!OS_ARCHES[@]}"; do
    os_arch=${OS_ARCHES[$i]}
    cgo_cc=${CGO_ARGS[$i]}
    os=${os_arch%%-*}
    arch=${os_arch##*-}
    export GOOS=$os
    export GOARCH=$arch
    export CC=${cgo_cc}
    echo "building for $os_arch"
    go build -o build/$os/$arch/"$appName" -ldflags="$docker_lflags" -tags=jsoniter .
  done

  DOCKER_ARM_ARCHES=(linux-arm/v6 linux-arm/v7)
  CGO_ARGS=(armv6-linux-musleabihf-gcc armv7l-linux-musleabihf-gcc)
  GO_ARM=(6 7)
  export GOOS=linux
  export GOARCH=arm
  for i in "${!DOCKER_ARM_ARCHES[@]}"; do
    docker_arch=${DOCKER_ARM_ARCHES[$i]}
    cgo_cc=${CGO_ARGS[$i]}
    export GOARM=${GO_ARM[$i]}
    export CC=${cgo_cc}
    echo "building for $docker_arch"
    go build -o build/${docker_arch%%-*}/${docker_arch##*-}/"$appName" -ldflags="$docker_lflags" -tags=jsoniter .
  done
}

BuildRelease() {
  rm -rf .git/
  mkdir -p "build"
  BuildWinArm64 ./build/"$appName"-windows-arm64.exe
  BuildWin7 ./build/"$appName"-windows7
  xgo -out "$appName" -ldflags="$ldflags" -tags=jsoniter .
  # why? Because some target platforms seem to have issues with upx compression
  # upx -9 ./"$appName"-linux-amd64
  # cp ./"$appName"-windows-amd64.exe ./"$appName"-windows-amd64-upx.exe
  # upx -9 ./"$appName"-windows-amd64-upx.exe
  mv "$appName"-* build
  
  # Build LoongArch with glibc (both old world abi1.0 and new world abi2.0)
  # Separate from musl builds to avoid cache conflicts
  BuildLoongGLIBC ./build/$appName-linux-loong64-abi1.0 abi1.0
  BuildLoongGLIBC ./build/$appName-linux-loong64 abi2.0
}

BuildLoongGLIBC() {
  local target_abi="$2"
  local output_file="$1"
  local oldWorldGoVersion="1.25.0"
  
  if [ "$target_abi" = "abi1.0" ]; then
    echo building for linux-loong64-abi1.0
  else
    echo building for linux-loong64-abi2.0
    target_abi="abi2.0"  # Default to abi2.0 if not specified
  fi
  
  # Note: No longer need global cache cleanup since ABI1.0 uses isolated cache directory
  echo "Using optimized cache strategy: ABI1.0 has isolated cache, ABI2.0 uses standard cache"
  
  if [ "$target_abi" = "abi1.0" ]; then
    # Setup abi1.0 toolchain and patched Go compiler similar to cgo-action implementation
    echo "Setting up Loongson old-world ABI1.0 toolchain and patched Go compiler..."
    
    # Download and setup patched Go compiler for old-world
    if ! curl -fsSL --retry 3 -H "Authorization: Bearer $GITHUB_TOKEN" \
      "https://github.com/loong64/loong64-abi1.0-toolchains/releases/download/20250821/go${oldWorldGoVersion}.linux-amd64.tar.gz" \
      -o go-loong64-abi1.0.tar.gz; then
      echo "Error: Failed to download patched Go compiler for old-world ABI1.0"
      if [ -n "$GITHUB_TOKEN" ]; then
        echo "Error output from curl:"
        curl -fsSL --retry 3 -H "Authorization: Bearer $GITHUB_TOKEN" \
          "https://github.com/loong64/loong64-abi1.0-toolchains/releases/download/20250821/go${oldWorldGoVersion}.linux-amd64.tar.gz" \
          -o go-loong64-abi1.0.tar.gz || true
      fi
      return 1
    fi
    
    rm -rf go-loong64-abi1.0
    mkdir go-loong64-abi1.0
    if ! tar -xzf go-loong64-abi1.0.tar.gz -C go-loong64-abi1.0 --strip-components=1; then
      echo "Error: Failed to extract patched Go compiler"
      return 1
    fi
    rm go-loong64-abi1.0.tar.gz
    
    # Download and setup GCC toolchain for old-world
    if ! curl -fsSL --retry 3 -H "Authorization: Bearer $GITHUB_TOKEN" \
      "https://github.com/loong64/loong64-abi1.0-toolchains/releases/download/20250722/loongson-gnu-toolchain-8.3.novec-x86_64-loongarch64-linux-gnu-rc1.1.tar.xz" \
      -o gcc8-loong64-abi1.0.tar.xz; then
      echo "Error: Failed to download GCC toolchain for old-world ABI1.0"
      if [ -n "$GITHUB_TOKEN" ]; then
        echo "Error output from curl:"
        curl -fsSL --retry 3 -H "Authorization: Bearer $GITHUB_TOKEN" \
          "https://github.com/loong64/loong64-abi1.0-toolchains/releases/download/20250722/loongson-gnu-toolchain-8.3.novec-x86_64-loongarch64-linux-gnu-rc1.1.tar.xz" \
          -o gcc8-loong64-abi1.0.tar.xz || true
      fi
      return 1
    fi
    
    rm -rf gcc8-loong64-abi1.0
    mkdir gcc8-loong64-abi1.0
    if ! tar -Jxf gcc8-loong64-abi1.0.tar.xz -C gcc8-loong64-abi1.0 --strip-components=1; then
      echo "Error: Failed to extract GCC toolchain"
      return 1
    fi
    rm gcc8-loong64-abi1.0.tar.xz
    
    # Setup separate cache directory for ABI1.0 to avoid cache pollution
    abi1_cache_dir="$(pwd)/go-loong64-abi1.0-cache"
    mkdir -p "$abi1_cache_dir"
    echo "Using separate cache directory for ABI1.0: $abi1_cache_dir"
    
    # Use patched Go compiler for old-world build (critical for ABI1.0 compatibility)
    echo "Building with patched Go compiler for old-world ABI1.0..."
    echo "Using isolated cache directory: $abi1_cache_dir"
    
    # Use env command to set environment variables locally without affecting global environment
    if ! env GOOS=linux GOARCH=loong64 \
        CC="$(pwd)/gcc8-loong64-abi1.0/bin/loongarch64-linux-gnu-gcc" \
        CXX="$(pwd)/gcc8-loong64-abi1.0/bin/loongarch64-linux-gnu-g++" \
        CGO_ENABLED=1 \
        GOCACHE="$abi1_cache_dir" \
        $(pwd)/go-loong64-abi1.0/bin/go build -a -o "$output_file" -ldflags="$ldflags" -tags=jsoniter .; then
      echo "Error: Build failed with patched Go compiler"
      echo "Attempting retry with cache cleanup..."
      env GOCACHE="$abi1_cache_dir" $(pwd)/go-loong64-abi1.0/bin/go clean -cache
      if ! env GOOS=linux GOARCH=loong64 \
          CC="$(pwd)/gcc8-loong64-abi1.0/bin/loongarch64-linux-gnu-gcc" \
          CXX="$(pwd)/gcc8-loong64-abi1.0/bin/loongarch64-linux-gnu-g++" \
          CGO_ENABLED=1 \
          GOCACHE="$abi1_cache_dir" \
          $(pwd)/go-loong64-abi1.0/bin/go build -a -o "$output_file" -ldflags="$ldflags" -tags=jsoniter .; then
        echo "Error: Build failed again after cache cleanup"
        echo "Build environment details:"
        echo "GOOS=linux"
        echo "GOARCH=loong64" 
        echo "CC=$(pwd)/gcc8-loong64-abi1.0/bin/loongarch64-linux-gnu-gcc"
        echo "CXX=$(pwd)/gcc8-loong64-abi1.0/bin/loongarch64-linux-gnu-g++"
        echo "CGO_ENABLED=1"
        echo "GOCACHE=$abi1_cache_dir"
        echo "Go version: $($(pwd)/go-loong64-abi1.0/bin/go version)"
        echo "GCC version: $($(pwd)/gcc8-loong64-abi1.0/bin/loongarch64-linux-gnu-gcc --version | head -1)"
        return 1
      fi
    fi
  else
    # Setup abi2.0 toolchain for new world glibc build
    echo "Setting up new-world ABI2.0 toolchain..."
    if ! curl -fsSL --retry 3 -H "Authorization: Bearer $GITHUB_TOKEN" \
      "https://github.com/loong64/cross-tools/releases/download/20250507/x86_64-cross-tools-loongarch64-unknown-linux-gnu-legacy.tar.xz" \
      -o gcc12-loong64-abi2.0.tar.xz; then
      echo "Error: Failed to download GCC toolchain for new-world ABI2.0"
      if [ -n "$GITHUB_TOKEN" ]; then
        echo "Error output from curl:"
        curl -fsSL --retry 3 -H "Authorization: Bearer $GITHUB_TOKEN" \
          "https://github.com/loong64/cross-tools/releases/download/20250507/x86_64-cross-tools-loongarch64-unknown-linux-gnu-legacy.tar.xz" \
          -o gcc12-loong64-abi2.0.tar.xz || true
      fi
      return 1
    fi
    
    rm -rf gcc12-loong64-abi2.0
    mkdir gcc12-loong64-abi2.0
    if ! tar -Jxf gcc12-loong64-abi2.0.tar.xz -C gcc12-loong64-abi2.0 --strip-components=1; then
      echo "Error: Failed to extract GCC toolchain"
      return 1
    fi
    rm gcc12-loong64-abi2.0.tar.xz
    
    export GOOS=linux
    export GOARCH=loong64
    export CC=$(pwd)/gcc12-loong64-abi2.0/bin/loongarch64-unknown-linux-gnu-gcc
    export CXX=$(pwd)/gcc12-loong64-abi2.0/bin/loongarch64-unknown-linux-gnu-g++
    export CGO_ENABLED=1
    
    # Use standard Go compiler for new-world build
    echo "Building with standard Go compiler for new-world ABI2.0..."
    if ! go build -a -o "$output_file" -ldflags="$ldflags" -tags=jsoniter .; then
      echo "Error: Build failed with standard Go compiler"
      echo "Attempting retry with cache cleanup..."
      go clean -cache
      if ! go build -a -o "$output_file" -ldflags="$ldflags" -tags=jsoniter .; then
        echo "Error: Build failed again after cache cleanup"
        echo "Build environment details:"
        echo "GOOS=$GOOS"
        echo "GOARCH=$GOARCH"
        echo "CC=$CC"
        echo "CXX=$CXX"
        echo "CGO_ENABLED=$CGO_ENABLED"
        echo "Go version: $(go version)"
        echo "GCC version: $($CC --version | head -1)"
        return 1
      fi
    fi
  fi
}

BuildReleaseLinuxMusl() {
  rm -rf .git/
  mkdir -p "build"
  muslflags="--extldflags '-static -fpic' $ldflags"
  BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/"
  FILES=(x86_64-linux-musl-cross aarch64-linux-musl-cross mips-linux-musl-cross mips64-linux-musl-cross mips64el-linux-musl-cross mipsel-linux-musl-cross powerpc64le-linux-musl-cross s390x-linux-musl-cross loongarch64-linux-musl-cross)
  for i in "${FILES[@]}"; do
    url="${BASE}${i}.tgz"
    curl -fsSL -o "${i}.tgz" "${url}"
    sudo tar xf "${i}.tgz" --strip-components 1 -C /usr/local
    rm -f "${i}.tgz"
  done
  OS_ARCHES=(linux-musl-amd64 linux-musl-arm64 linux-musl-mips linux-musl-mips64 linux-musl-mips64le linux-musl-mipsle linux-musl-ppc64le linux-musl-s390x linux-musl-loong64)
  CGO_ARGS=(x86_64-linux-musl-gcc aarch64-linux-musl-gcc mips-linux-musl-gcc mips64-linux-musl-gcc mips64el-linux-musl-gcc mipsel-linux-musl-gcc powerpc64le-linux-musl-gcc s390x-linux-musl-gcc loongarch64-linux-musl-gcc)
  for i in "${!OS_ARCHES[@]}"; do
    os_arch=${OS_ARCHES[$i]}
    cgo_cc=${CGO_ARGS[$i]}
    echo building for ${os_arch}
    export GOOS=${os_arch%%-*}
    export GOARCH=${os_arch##*-}
    export CC=${cgo_cc}
    export CGO_ENABLED=1
    go build -o ./build/$appName-$os_arch -ldflags="$muslflags" -tags=jsoniter .
  done
}

BuildReleaseLinuxMuslArm() {
  rm -rf .git/
  mkdir -p "build"
  muslflags="--extldflags '-static -fpic' $ldflags"
  BASE="https://github.com/OpenListTeam/musl-compilers/releases/latest/download/"
  FILES=(arm-linux-musleabi-cross arm-linux-musleabihf-cross armel-linux-musleabi-cross armel-linux-musleabihf-cross armv5l-linux-musleabi-cross armv5l-linux-musleabihf-cross armv6-linux-musleabi-cross armv6-linux-musleabihf-cross armv7l-linux-musleabihf-cross armv7m-linux-musleabi-cross armv7r-linux-musleabihf-cross)
  for i in "${FILES[@]}"; do
    url="${BASE}${i}.tgz"
    curl -fsSL -o "${i}.tgz" "${url}"
    sudo tar xf "${i}.tgz" --strip-components 1 -C /usr/local
    rm -f "${i}.tgz"
  done
  OS_ARCHES=(linux-musleabi-arm linux-musleabihf-arm linux-musleabi-armel linux-musleabihf-armel linux-musleabi-armv5l linux-musleabihf-armv5l linux-musleabi-armv6 linux-musleabihf-armv6 linux-musleabihf-armv7l linux-musleabi-armv7m linux-musleabihf-armv7r)
  CGO_ARGS=(arm-linux-musleabi-gcc arm-linux-musleabihf-gcc armel-linux-musleabi-gcc armel-linux-musleabihf-gcc armv5l-linux-musleabi-gcc armv5l-linux-musleabihf-gcc armv6-linux-musleabi-gcc armv6-linux-musleabihf-gcc armv7l-linux-musleabihf-gcc armv7m-linux-musleabi-gcc armv7r-linux-musleabihf-gcc)
  GOARMS=('' '' '' '' '5' '5' '6' '6' '7' '7' '7')
  for i in "${!OS_ARCHES[@]}"; do
    os_arch=${OS_ARCHES[$i]}
    cgo_cc=${CGO_ARGS[$i]}
    arm=${GOARMS[$i]}
    echo building for ${os_arch}
    export GOOS=linux
    export GOARCH=arm
    export CC=${cgo_cc}
    export CGO_ENABLED=1
    export GOARM=${arm}
    go build -o ./build/$appName-$os_arch -ldflags="$muslflags" -tags=jsoniter .
  done
}


BuildReleaseAndroid() {
  rm -rf .git/
  mkdir -p "build"
  wget https://dl.google.com/android/repository/android-ndk-r26b-linux.zip
  unzip android-ndk-r26b-linux.zip
  rm android-ndk-r26b-linux.zip
  OS_ARCHES=(amd64 arm64 386 arm)
  CGO_ARGS=(x86_64-linux-android24-clang aarch64-linux-android24-clang i686-linux-android24-clang armv7a-linux-androideabi24-clang)
  for i in "${!OS_ARCHES[@]}"; do
    os_arch=${OS_ARCHES[$i]}
    cgo_cc=$(realpath android-ndk-r26b/toolchains/llvm/prebuilt/linux-x86_64/bin/${CGO_ARGS[$i]})
    echo building for android-${os_arch}
    export GOOS=android
    export GOARCH=${os_arch##*-}
    export CC=${cgo_cc}
    export CGO_ENABLED=1
    go build -o ./build/$appName-android-$os_arch -ldflags="$ldflags" -tags=jsoniter .
    android-ndk-r26b/toolchains/llvm/prebuilt/linux-x86_64/bin/llvm-strip ./build/$appName-android-$os_arch
  done
}

BuildReleaseFreeBSD() {
  rm -rf .git/
  mkdir -p "build/freebsd"
  
  # Get latest FreeBSD 14.x release version from GitHub 
  freebsd_version=$(eval "curl -fsSL --max-time 2 $githubAuthArgs \"https://api.github.com/repos/freebsd/freebsd-src/tags\"" | \
    jq -r '.[].name' | \
    grep '^release/14\.' | \
    grep -v -- '-p[0-9]*$' | \
    sort -V | \
    tail -1 | \
    sed 's/release\///' | \
    sed 's/\.0$//')
  
  if [ -z "$freebsd_version" ]; then
    echo "Failed to get FreeBSD version, falling back to 14.3"
    freebsd_version="14.3"
  fi

  echo "Using FreeBSD version: $freebsd_version"
  
  OS_ARCHES=(amd64 arm64 i386)
  GO_ARCHES=(amd64 arm64 386)
  CGO_ARGS=(x86_64-unknown-freebsd${freebsd_version} aarch64-unknown-freebsd${freebsd_version} i386-unknown-freebsd${freebsd_version})
  for i in "${!OS_ARCHES[@]}"; do
    os_arch=${OS_ARCHES[$i]}
    cgo_cc="clang --target=${CGO_ARGS[$i]} --sysroot=/opt/freebsd/${os_arch}"
    echo building for freebsd-${os_arch}
    sudo mkdir -p "/opt/freebsd/${os_arch}"
    wget -q https://download.freebsd.org/releases/${os_arch}/${freebsd_version}-RELEASE/base.txz
    sudo tar -xf ./base.txz -C /opt/freebsd/${os_arch}
    rm base.txz
    export GOOS=freebsd
    export GOARCH=${GO_ARCHES[$i]}
    export CC=${cgo_cc}
    export CGO_ENABLED=1
    export CGO_LDFLAGS="-fuse-ld=lld"
    go build -o ./build/$appName-freebsd-$os_arch -ldflags="$ldflags" -tags=jsoniter .
  done
}

MakeRelease() {
  cd build
  if [ -d compress ]; then
    rm -rv compress
  fi
  mkdir compress
  
  # Add -lite suffix if useLite is true
  liteSuffix=""
  if [ "$useLite" = true ]; then
    liteSuffix="-lite"
  fi
  
  for i in $(find . -type f -name "$appName-linux-*"); do
    cp "$i" "$appName"
    tar -czvf compress/"$i$liteSuffix".tar.gz "$appName"
    rm -f "$appName"
  done
    for i in $(find . -type f -name "$appName-android-*"); do
    cp "$i" "$appName"
    tar -czvf compress/"$i$liteSuffix".tar.gz "$appName"
    rm -f "$appName"
  done
  for i in $(find . -type f -name "$appName-darwin-*"); do
    cp "$i" "$appName"
    tar -czvf compress/"$i$liteSuffix".tar.gz "$appName"
    rm -f "$appName"
  done
  for i in $(find . -type f -name "$appName-freebsd-*"); do
    cp "$i" "$appName"
    tar -czvf compress/"$i$liteSuffix".tar.gz "$appName"
    rm -f "$appName"
  done
  for i in $(find . -type f \( -name "$appName-windows-*" -o -name "$appName-windows7-*" \)); do
    cp "$i" "$appName".exe
    zip compress/$(echo $i | sed 's/\.[^.]*$//')$liteSuffix.zip "$appName".exe
    rm -f "$appName".exe
  done
  cd compress
  
  # Handle MD5 filename - add -lite suffix only if not already present
  md5FileName="$1"
  if [ "$useLite" = true ] && [[ "$1" != *"-lite.txt" ]]; then
    md5FileName=$(echo "$1" | sed 's/\.txt$/-lite.txt/')
  fi
  
  find . -type f -print0 | xargs -0 md5sum >"$md5FileName"
  cat "$md5FileName"
  cd ../..
}

# Parse parameters to handle lite parameter position flexibility
buildType=""
dockerType=""
otherParam=""

for arg in "$@"; do
  case $arg in
    dev|beta|release|zip|prepare)
      if [ -z "$buildType" ]; then
        buildType="$arg"
      fi
      ;;
    docker|docker-multiplatform|linux_musl_arm|linux_musl|android|freebsd|web)
      if [ -z "$dockerType" ]; then
        dockerType="$arg"
      fi
      ;;
    lite)
      # lite parameter is already handled above
      ;;
    *)
      if [ -z "$otherParam" ]; then
        otherParam="$arg"
      fi
      ;;
  esac
done

if [ "$buildType" = "dev" ]; then
  FetchWebRolling
  if [ "$dockerType" = "docker" ]; then
    BuildDocker
  elif [ "$dockerType" = "docker-multiplatform" ]; then
      BuildDockerMultiplatform
  elif [ "$dockerType" = "web" ]; then
    echo "web only"
  else
    BuildDev
  fi
elif [ "$buildType" = "release" -o "$buildType" = "beta" ]; then
  if [ "$buildType" = "beta" ]; then
    FetchWebRolling
  else
    FetchWebRelease
  fi
  if [ "$dockerType" = "docker" ]; then
    BuildDocker
  elif [ "$dockerType" = "docker-multiplatform" ]; then
    BuildDockerMultiplatform
  elif [ "$dockerType" = "linux_musl_arm" ]; then
    BuildReleaseLinuxMuslArm
    if [ "$useLite" = true ]; then
      MakeRelease "md5-linux-musl-arm-lite.txt"
    else
      MakeRelease "md5-linux-musl-arm.txt"
    fi
  elif [ "$dockerType" = "linux_musl" ]; then
    BuildReleaseLinuxMusl
    if [ "$useLite" = true ]; then
      MakeRelease "md5-linux-musl-lite.txt"
    else
      MakeRelease "md5-linux-musl.txt"
    fi
  elif [ "$dockerType" = "android" ]; then
    BuildReleaseAndroid
    if [ "$useLite" = true ]; then
      MakeRelease "md5-android-lite.txt"
    else
      MakeRelease "md5-android.txt"
    fi
  elif [ "$dockerType" = "freebsd" ]; then
    BuildReleaseFreeBSD
    if [ "$useLite" = true ]; then
      MakeRelease "md5-freebsd-lite.txt"
    else
      MakeRelease "md5-freebsd.txt"
    fi
  elif [ "$dockerType" = "web" ]; then
    echo "web only"
  else
    BuildRelease
    if [ "$useLite" = true ]; then
      MakeRelease "md5-lite.txt"
    else
      MakeRelease "md5.txt"
    fi
  fi
elif [ "$buildType" = "prepare" ]; then
  if [ "$dockerType" = "docker-multiplatform" ]; then
    PrepareBuildDockerMusl
  fi
elif [ "$buildType" = "zip" ]; then
  if [ -n "$otherParam" ]; then
    if [ "$useLite" = true ]; then
      MakeRelease "$otherParam-lite.txt"
    else
      MakeRelease "$otherParam.txt"
    fi
  elif [ -n "$dockerType" ]; then
    if [ "$useLite" = true ]; then
      MakeRelease "$dockerType-lite.txt"
    else
      MakeRelease "$dockerType.txt"
    fi
  else
    if [ "$useLite" = true ]; then
      MakeRelease "md5-lite.txt"
    else
      MakeRelease "md5.txt"
    fi
  fi
else
  echo -e "Parameter error"
  echo -e "Usage: $0 {dev|beta|release|zip|prepare} [docker|docker-multiplatform|linux_musl_arm|linux_musl|android|freebsd|web] [lite] [other_params]"
  echo -e "Examples:"
  echo -e "  $0 dev"
  echo -e "  $0 dev lite"
  echo -e "  $0 dev docker"
  echo -e "  $0 dev docker lite"
  echo -e "  $0 release"
  echo -e "  $0 release lite"
  echo -e "  $0 release docker lite"
  echo -e "  $0 release linux_musl"
fi

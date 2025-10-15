include("${CMAKE_CURRENT_LIST_DIR}/base-linux.cmake")

set(VCPKG_TARGET_ARCHITECTURE arm64)

set(VCPKG_CHAINLOAD_TOOLCHAIN_FILE ${CMAKE_CURRENT_LIST_DIR}/toolchains/arm64-linux.cmake)

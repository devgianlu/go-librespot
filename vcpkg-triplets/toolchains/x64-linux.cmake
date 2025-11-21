include("${CMAKE_CURRENT_LIST_DIR}/base-linux.cmake")

set(CMAKE_SYSTEM_PROCESSOR x64)

if(NOT CMAKE_HOST_SYSTEM_NAME STREQUAL "Linux" OR NOT CMAKE_HOST_SYSTEM_PROCESSOR STREQUAL "x86_64")
    find_program(CMAKE_C_COMPILER NAMES x86_64-linux-gnu-gcc)
    if(NOT CMAKE_C_COMPILER)
        message(FATAL_ERROR "Could not find x86_64-linux-gnu-gcc. Please install the appropriate cross compiler toolchain.")
    endif()
else()
    set(CMAKE_C_COMPILER gcc)
endif()

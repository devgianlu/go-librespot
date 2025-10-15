include("${CMAKE_CURRENT_LIST_DIR}/base-linux.cmake")

set(CMAKE_SYSTEM_PROCESSOR arm)

find_program(CMAKE_C_COMPILER NAMES arm-rpi-linux-gnueabihf-gcc)
if(NOT CMAKE_C_COMPILER)
    message(FATAL_ERROR "Could not find arm-rpi-linux-gnueabihf-gcc. Please install the appropriate cross compiler toolchain
      from https://github.com/devgianlu/rpi-toolchain.")
endif()

# Windows native backend, MSVC build → build/msvc/rcnative.dll + rcnative.lib.
#
# cgo on Windows links with mingw, which cannot statically link MSVC C++
# objects (CRT, EH and security-cookie ABI differ), so the MSVC variant is a
# DLL behind the plain C ABI of rc_native.h. Go links it with the
# `rcnative_msvc` build tag and the MSI ships rcnative.dll next to the exe.
# This variant enables the Windows.Graphics.Capture path (C++/WinRT).
# The default build is Makefile.mingw (static, DXGI duplication only).
#
#   cmake -S native/windows -B native/windows/build/cmake -A x64
#   cmake --build native/windows/build/cmake --config Release
cmake_minimum_required(VERSION 3.24)
project(rcnative C CXX)

option(RC_WGC "Enable the Windows.Graphics.Capture path (needs C++/WinRT from the Windows SDK)" ON)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_MSVC_RUNTIME_LIBRARY "MultiThreaded$<$<CONFIG:Debug>:Debug>")

file(GLOB RC_SOURCES ${CMAKE_CURRENT_SOURCE_DIR}/src/*.cpp)
add_library(rcnative SHARED ${RC_SOURCES} ${CMAKE_CURRENT_SOURCE_DIR}/../common/rc_nv12.c
            ${CMAKE_CURRENT_SOURCE_DIR}/rcnative.def)
target_compile_definitions(rcnative PRIVATE UNICODE _UNICODE _WIN32_WINNT=0x0A00 WINVER=0x0A00 NOMINMAX WIN32_LEAN_AND_MEAN)
target_include_directories(rcnative PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/../include)
target_include_directories(rcnative PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/../common)
target_link_libraries(rcnative PRIVATE d3d11 dxgi mfuuid ole32 oleaut32 user32 gdi32 winmm wtsapi32 advapi32)
if(MSVC)
  target_compile_options(rcnative PRIVATE /W4 /permissive- /Zc:__cplusplus /EHsc /GS /guard:cf /utf-8)
  target_link_options(rcnative PRIVATE /guard:cf)
endif()
if(RC_WGC)
  target_compile_definitions(rcnative PRIVATE RC_HAVE_WGC=1)
  target_link_libraries(rcnative PRIVATE windowsapp)
endif()
set(RC_OUT ${CMAKE_CURRENT_SOURCE_DIR}/build/msvc)
set_target_properties(rcnative PROPERTIES
  RUNTIME_OUTPUT_DIRECTORY ${RC_OUT} RUNTIME_OUTPUT_DIRECTORY_RELEASE ${RC_OUT} RUNTIME_OUTPUT_DIRECTORY_DEBUG ${RC_OUT}
  ARCHIVE_OUTPUT_DIRECTORY ${RC_OUT} ARCHIVE_OUTPUT_DIRECTORY_RELEASE ${RC_OUT} ARCHIVE_OUTPUT_DIRECTORY_DEBUG ${RC_OUT})

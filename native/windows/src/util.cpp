#include "rc_win.h"

#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>

namespace rcw {

static rc_log_fn volatile g_log_fn = nullptr;

void set_log(rc_log_fn fn) { g_log_fn = fn; }

void logf(const char *fmt, ...) {
    char buf[1024];
    va_list ap;
    va_start(ap, fmt);
    int n = vsnprintf(buf, sizeof(buf) - 2, fmt, ap);
    va_end(ap);
    if (n < 0) return;
    if (n > (int)sizeof(buf) - 2) n = (int)sizeof(buf) - 2;
    buf[n] = 0;
    rc_log_fn fn = g_log_fn;
    if (fn) fn(buf);
    buf[n] = '\n';
    buf[n + 1] = 0;
    OutputDebugStringA(buf);
    static int to_stderr = -1;
    if (to_stderr < 0) to_stderr = GetEnvironmentVariableA("RC_NATIVE_DEBUG", nullptr, 0) > 0 ? 1 : 0;
    if (to_stderr) {
        fputs("[rcnative] ", stderr);
        fputs(buf, stderr);
    }
}

wchar_t *utf8_to_wide(const char *s, int32_t len) {
    if (!s) return nullptr;
    if (len < 0) len = (int32_t)strlen(s);
    int n = MultiByteToWideChar(CP_UTF8, 0, s, len, nullptr, 0);
    wchar_t *w = (wchar_t *)malloc(sizeof(wchar_t) * (size_t)(n + 1));
    if (!w) return nullptr;
    if (n > 0) MultiByteToWideChar(CP_UTF8, 0, s, len, w, n);
    w[n] = 0;
    return w;
}

void wide_to_utf8(const wchar_t *w, char *out, int cap) {
    if (cap <= 0) return;
    out[0] = 0;
    if (!w) return;
    int n = WideCharToMultiByte(CP_UTF8, 0, w, -1, out, cap, nullptr, nullptr);
    if (n <= 0) out[cap - 1] = 0;
}

bool os_before_win8() {
    static int before = -1;
    if (before < 0) {
        // RtlGetVersion, unlike GetVersionEx, is not shimmed to the manifest's version.
        typedef LONG(WINAPI * RtlGetVersionFn)(OSVERSIONINFOW *);
        OSVERSIONINFOW v = {};
        v.dwOSVersionInfoSize = sizeof(v);
        HMODULE ntdll = GetModuleHandleW(L"ntdll.dll");
        RtlGetVersionFn fn = ntdll ? (RtlGetVersionFn)(void *)GetProcAddress(ntdll, "RtlGetVersion") : nullptr;
        before = fn && fn(&v) == 0 && (v.dwMajorVersion < 6 || (v.dwMajorVersion == 6 && v.dwMinorVersion < 2)) ? 1 : 0;
    }
    return before == 1;
}

HMODULE load_system_dll(const wchar_t *name) {
    HMODULE m = LoadLibraryExW(name, nullptr, LOAD_LIBRARY_SEARCH_SYSTEM32);
    if (m || GetLastError() != ERROR_INVALID_PARAMETER) return m;
    // Windows 7 without KB2533623 has no LOAD_LIBRARY_SEARCH_*: use the full path.
    wchar_t path[MAX_PATH];
    UINT n = GetSystemDirectoryW(path, MAX_PATH);
    if (n == 0 || n + 1 + wcslen(name) >= MAX_PATH) return nullptr;
    path[n] = L'\\';
    wcscpy(path + n + 1, name);
    return LoadLibraryW(path);
}

int64_t now_us() {
    static LARGE_INTEGER freq = {};
    if (freq.QuadPart == 0) QueryPerformanceFrequency(&freq);
    LARGE_INTEGER c;
    QueryPerformanceCounter(&c);
    return (int64_t)((c.QuadPart / freq.QuadPart) * 1000000 +
                     (c.QuadPart % freq.QuadPart) * 1000000 / freq.QuadPart);
}

static void desktop_name(HDESK d, wchar_t *name, DWORD cap) {
    name[0] = 0;
    DWORD needed = 0;
    GetUserObjectInformationW(d, UOI_NAME, name, cap * sizeof(wchar_t), &needed);
}

bool attach_input_desktop() {
    // Per-thread: the last input desktop we attached to and when we checked.
    static thread_local HDESK attached = nullptr;
    static thread_local int64_t checked_at = 0;
    static thread_local wchar_t attached_name[64] = {0};

    int64_t now = now_us();
    if (attached && now - checked_at < 250000) return true;
    checked_at = now;

    HDESK input = OpenInputDesktop(0, FALSE, GENERIC_ALL);
    if (!input) return attached != nullptr;
    wchar_t name[64];
    desktop_name(input, name, 64);
    if (attached && wcscmp(name, attached_name) == 0) {
        CloseDesktop(input);
        return true;
    }
    if (!SetThreadDesktop(input)) {
        // Fails when the thread owns windows or hooks; stay where we are.
        CloseDesktop(input);
        return false;
    }
    if (attached) CloseDesktop(attached);
    attached = input;
    wcsncpy(attached_name, name, 63);
    attached_name[63] = 0;
    return true;
}

bool input_desktop_is_default() {
    HDESK input = OpenInputDesktop(0, FALSE, DESKTOP_READOBJECTS);
    if (!input) return false; // no access usually means a secure desktop
    wchar_t name[64];
    desktop_name(input, name, 64);
    CloseDesktop(input);
    return _wcsicmp(name, L"Default") == 0;
}

} // namespace rcw

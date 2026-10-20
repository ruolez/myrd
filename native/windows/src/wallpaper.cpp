// rc_wallpaper_hide: the wallpaper off/on for the console user's logon session.
// SPI_SETDESKWALLPAPER without SPIF_UPDATEINIFILE never touches the registry,
// so logoff restores the wallpaper even if the helper dies, and "" / NULL are
// the documented off / reload-from-registry values. The helper runs as SYSTEM,
// so every call impersonates the console user: the reload must read their
// hive, not SYSTEM's.
#include "rc_win.h"

#include <wtsapi32.h>

namespace rcw {

static Lock g_wp_lock;
static bool g_wp_hidden;

template <class Fn>
static bool as_console_user(Fn fn) {
    HANDLE tok = nullptr;
    if (!WTSQueryUserToken(WTSGetActiveConsoleSessionId(), &tok)) {
        logf("wallpaper: no console user token (%lu)", GetLastError());
        return false;
    }
    bool ok = false;
    if (ImpersonateLoggedOnUser(tok)) {
        ok = fn();
        RevertToSelf();
    } else {
        logf("wallpaper: impersonation failed (%lu)", GetLastError());
    }
    CloseHandle(tok);
    return ok;
}

static bool set_wallpaper(const wchar_t *path) {
    if (SystemParametersInfoW(SPI_SETDESKWALLPAPER, 0, (void *)path, 0)) return true;
    logf("wallpaper: SPI_SETDESKWALLPAPER failed (%lu)", GetLastError());
    return false;
}

// The user's saved wallpaper path is non-empty while the session shows none:
// a helper that hid it died before showing it again.
static bool hidden_by_lost_helper() {
    wchar_t cur[MAX_PATH] = {0};
    if (!SystemParametersInfoW(SPI_GETDESKWALLPAPER, MAX_PATH, cur, 0) || cur[0]) return false;
    HKEY hkcu = nullptr;
    if (RegOpenCurrentUser(KEY_READ, &hkcu) != ERROR_SUCCESS) return false;
    wchar_t saved[MAX_PATH] = {0};
    DWORD size = sizeof(saved);
    LSTATUS st = RegGetValueW(hkcu, L"Control Panel\\Desktop", L"Wallpaper", RRF_RT_REG_SZ, nullptr, saved, &size);
    RegCloseKey(hkcu);
    return st == ERROR_SUCCESS && saved[0];
}

void wallpaper_recover() {
    Guard g(g_wp_lock);
    as_console_user([] { return hidden_by_lost_helper() && set_wallpaper(nullptr); });
}

int wallpaper_hide(bool hide) {
    Guard g(g_wp_lock);
    if (hide == g_wp_hidden) return RC_OK;
    if (!as_console_user([hide] { return set_wallpaper(hide ? L"" : nullptr); })) return RC_ERR;
    g_wp_hidden = hide;
    return RC_OK;
}

} // namespace rcw

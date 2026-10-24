#include "rc_win.h"

#include <dxgi1_2.h>

#include <algorithm>

namespace rcw {

static Lock g_mon_lock;
static MonitorInfo g_mon_cache[16];
static int g_mon_count = 0;

typedef HRESULT(WINAPI *GetDpiForMonitorFn)(HMONITOR, int, UINT *, UINT *);

static float monitor_scale(HMONITOR hmon) {
    static GetDpiForMonitorFn fn = nullptr;
    static bool loaded = false;
    if (!loaded) {
        loaded = true;
        HMODULE shcore = load_system_dll(L"shcore.dll"); // Windows 8.1+; absent on 7
        if (shcore) fn = (GetDpiForMonitorFn)(void *)GetProcAddress(shcore, "GetDpiForMonitor");
    }
    UINT dx = 96, dy = 96;
    if (fn && SUCCEEDED(fn(hmon, 0 /* MDT_EFFECTIVE_DPI */, &dx, &dy)) && dx > 0) return dx / 96.0f;
    return 1.0f;
}

static void describe(MonitorInfo &m, HMONITOR hmon) {
    m.pub.x = m.rect.left;
    m.pub.y = m.rect.top;
    m.pub.width = (uint32_t)(m.rect.right - m.rect.left);
    m.pub.height = (uint32_t)(m.rect.bottom - m.rect.top);
    m.pub.scale = monitor_scale(hmon);
    MONITORINFOEXW mi;
    mi.cbSize = sizeof(mi);
    if (GetMonitorInfoW(hmon, &mi)) {
        m.pub.primary = (mi.dwFlags & MONITORINFOF_PRIMARY) ? 1 : 0;
        if (!m.pub.name[0]) wide_to_utf8(mi.szDevice, m.pub.name, sizeof(m.pub.name));
    }
}

struct GdiEnum {
    MonitorInfo *found;
    int n;
};

static BOOL CALLBACK gdi_monitor(HMONITOR hmon, HDC, LPRECT rect, LPARAM p) {
    GdiEnum *e = (GdiEnum *)p;
    if (e->n >= 16) return FALSE;
    MonitorInfo &m = e->found[e->n];
    memset(&m, 0, sizeof(m));
    m.rect = *rect;
    m.hmon = hmon;
    m.gdi = true;
    m.pub.id = 0xff00u | (uint32_t)(e->n + 1); // DXGI ids keep the adapter below 255
    describe(m, hmon);
    e->n++;
    return TRUE;
}

int enumerate_monitors(MonitorInfo *out, int max) {
    MonitorInfo found[16];
    int n = 0;
    Com<IDXGIFactory1> factory;
    HRESULT hr = CreateDXGIFactory1(__uuidof(IDXGIFactory1), factory.put_void());
    if (FAILED(hr)) logf("monitors: CreateDXGIFactory1 failed 0x%08lx", (unsigned long)hr);
    Com<IDXGIAdapter1> adapter;
    for (UINT a = 0; factory && n < 16 && SUCCEEDED(factory->EnumAdapters1(a, adapter.put())); a++) {
        Com<IDXGIOutput> output;
        for (UINT o = 0; n < 16 && SUCCEEDED(adapter->EnumOutputs(o, output.put())); o++) {
            DXGI_OUTPUT_DESC d;
            if (FAILED(output->GetDesc(&d)) || !d.AttachedToDesktop) continue;
            MonitorInfo &m = found[n];
            memset(&m, 0, sizeof(m));
            m.rect = d.DesktopCoordinates;
            m.hmon = d.Monitor;
            m.adapter_index = a;
            m.output_index = o;
            // Stable for the life of the process as long as the topology holds.
            m.pub.id = (a << 8) | (o + 1);
            wide_to_utf8(d.DeviceName, m.pub.name, sizeof(m.pub.name));
            describe(m, d.Monitor);
            n++;
        }
    }
    if (n == 0) {
        // No DXGI output on the desktop (a server's display-only adapter, no
        // monitor attached): GDI still draws a desktop the GDI pipeline can copy.
        GdiEnum e = {found, 0};
        EnumDisplayMonitors(nullptr, nullptr, gdi_monitor, (LPARAM)&e);
        n = e.n;
        static int logged = -1;
        if (logged != n) {
            logged = n;
            logf("monitors: no DXGI output on the desktop, %d GDI monitor(s)", n);
        }
    }
    {
        Guard g(g_mon_lock);
        memcpy(g_mon_cache, found, sizeof(MonitorInfo) * (size_t)n);
        g_mon_count = n;
    }
    int w = n < max ? n : max;
    if (out && w > 0) memcpy(out, found, sizeof(MonitorInfo) * (size_t)w);
    return n;
}

bool find_monitor(uint32_t id, MonitorInfo *out) {
    for (int pass = 0; pass < 2; pass++) {
        {
            Guard g(g_mon_lock);
            for (int i = 0; i < g_mon_count; i++) {
                if (g_mon_cache[i].pub.id == id) {
                    *out = g_mon_cache[i];
                    return true;
                }
            }
        }
        if (pass == 0) enumerate_monitors(nullptr, 0);
    }
    return false;
}

bool find_primary_monitor(MonitorInfo *out) {
    MonitorInfo all[16];
    int n = enumerate_monitors(all, 16);
    for (int i = 0; i < n; i++) {
        if (all[i].pub.primary) {
            *out = all[i];
            return true;
        }
    }
    if (n <= 0) return false;
    *out = all[0];
    return true;
}

// ---------------------------------------------------------------- modes

// The GDI device (\\.\DISPLAYn) showing monitor id (0 = primary).
static bool monitor_device(uint32_t id, wchar_t (&dev)[CCHDEVICENAME]) {
    MonitorInfo m;
    if (!(id ? find_monitor(id, &m) : find_primary_monitor(&m))) return false;
    MONITORINFOEXW mi;
    mi.cbSize = sizeof(mi);
    if (!GetMonitorInfoW(m.hmon, &mi)) return false;
    memcpy(dev, mi.szDevice, sizeof(dev));
    return true;
}

static bool rotated(const DEVMODEW &dm) {
    return (dm.dmFields & DM_DISPLAYORIENTATION) &&
           (dm.dmDisplayOrientation == DMDO_90 || dm.dmDisplayOrientation == DMDO_270);
}

int display_modes(uint32_t id, rc_display_mode *out, int max, rc_display_mode *current) {
    attach_input_desktop();
    wchar_t dev[CCHDEVICENAME];
    if (!monitor_device(id, dev)) return RC_ERR;
    DEVMODEW now = {};
    now.dmSize = sizeof(now);
    if (!EnumDisplaySettingsExW(dev, ENUM_CURRENT_SETTINGS, &now, 0)) return RC_ERR_UNSUPPORTED;
    if (current) *current = {now.dmPelsWidth, now.dmPelsHeight, 1.0f};
    // Modes are listed in the panel's own orientation; report them as the
    // desktop is turned now, as the current mode is.
    bool turn = rotated(now);
    int n = 0;
    // The monitor's modes first; with only one (a server without a monitor:
    // Windows assumes a generic one), every mode the driver has.
    const DWORD passes[] = {0, EDS_RAWMODE};
    for (DWORD flags : passes) {
        n = 0;
        DEVMODEW dm = {};
        dm.dmSize = sizeof(dm);
        for (DWORD i = 0; n < max && EnumDisplaySettingsExW(dev, i, &dm, flags); i++) {
            if (dm.dmBitsPerPel != now.dmBitsPerPel || dm.dmPelsWidth < 800 || dm.dmPelsHeight < 600) continue;
            uint32_t w = turn ? dm.dmPelsHeight : dm.dmPelsWidth, h = turn ? dm.dmPelsWidth : dm.dmPelsHeight;
            bool dup = false;
            for (int j = 0; j < n && !dup; j++) dup = out[j].width == w && out[j].height == h;
            if (!dup) out[n++] = {w, h, 1.0f};
        }
        if (n > 1) break;
    }
    std::sort(out, out + n, [](const rc_display_mode &a, const rc_display_mode &b) {
        uint64_t aa = (uint64_t)a.width * a.height, bb = (uint64_t)b.width * b.height;
        return aa != bb ? aa > bb : a.width > b.width;
    });
    return n;
}

// Waits (up to 2 s) until monitor id reports w×h: DXGI and the desktop
// follow a mode change a moment after ChangeDisplaySettingsEx returns.
static void await_size(uint32_t id, uint32_t w, uint32_t h) {
    for (int i = 0; i < 20; i++) {
        MonitorInfo all[16];
        int n = enumerate_monitors(all, 16);
        for (int j = 0; j < n && j < 16; j++)
            if ((all[j].pub.id == id || (id == 0 && all[j].pub.primary)) && all[j].pub.width == w &&
                all[j].pub.height == h)
                return;
        Sleep(100);
    }
    logf("display: monitor %u did not report %ux%u", id, w, h);
}

int display_set_mode(uint32_t id, uint32_t w, uint32_t h) {
    attach_input_desktop();
    wchar_t dev[CCHDEVICENAME];
    if (!monitor_device(id, dev)) return RC_ERR;
    DEVMODEW dm = {};
    dm.dmSize = sizeof(dm);
    if (!EnumDisplaySettingsExW(dev, ENUM_CURRENT_SETTINGS, &dm, 0)) return RC_ERR_UNSUPPORTED;
    dm.dmPelsWidth = w;
    dm.dmPelsHeight = h;
    dm.dmFields = DM_PELSWIDTH | DM_PELSHEIGHT;
    // No CDS_UPDATEREGISTRY: a dynamic change, gone after a reboot or display_restore.
    LONG r = ChangeDisplaySettingsExW(dev, &dm, nullptr, 0, nullptr);
    char name[64];
    wide_to_utf8(dev, name, sizeof(name));
    if (r != DISP_CHANGE_SUCCESSFUL) {
        logf("display: %s %ux%u refused (%ld)", name, w, h, r);
        return RC_ERR;
    }
    logf("display: %s switched to %ux%u", name, w, h);
    await_size(id, w, h);
    return RC_OK;
}

int display_restore() {
    attach_input_desktop();
    // A null mode and no flags puts every display back on its registry mode.
    LONG r = ChangeDisplaySettingsExW(nullptr, nullptr, nullptr, 0, nullptr);
    if (r != DISP_CHANGE_SUCCESSFUL) {
        logf("display: restoring the saved modes failed (%ld)", r);
        return RC_ERR;
    }
    logf("display: saved modes restored");
    Sleep(300);
    enumerate_monitors(nullptr, 0);
    return RC_OK;
}

RECT virtual_desktop() {
    RECT r;
    r.left = GetSystemMetrics(SM_XVIRTUALSCREEN);
    r.top = GetSystemMetrics(SM_YVIRTUALSCREEN);
    r.right = r.left + GetSystemMetrics(SM_CXVIRTUALSCREEN);
    r.bottom = r.top + GetSystemMetrics(SM_CYVIRTUALSCREEN);
    return r;
}

} // namespace rcw

#include "rc_win.h"

#include <dxgi1_2.h>

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

RECT virtual_desktop() {
    RECT r;
    r.left = GetSystemMetrics(SM_XVIRTUALSCREEN);
    r.top = GetSystemMetrics(SM_YVIRTUALSCREEN);
    r.right = r.left + GetSystemMetrics(SM_CXVIRTUALSCREEN);
    r.bottom = r.top + GetSystemMetrics(SM_CYVIRTUALSCREEN);
    return r;
}

} // namespace rcw

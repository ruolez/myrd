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
        HMODULE shcore = LoadLibraryW(L"shcore.dll");
        if (shcore) fn = (GetDpiForMonitorFn)(void *)GetProcAddress(shcore, "GetDpiForMonitor");
    }
    UINT dx = 96, dy = 96;
    if (fn && SUCCEEDED(fn(hmon, 0 /* MDT_EFFECTIVE_DPI */, &dx, &dy)) && dx > 0) return dx / 96.0f;
    return 1.0f;
}

int enumerate_monitors(MonitorInfo *out, int max) {
    Com<IDXGIFactory1> factory;
    if (FAILED(CreateDXGIFactory1(__uuidof(IDXGIFactory1), factory.put_void()))) return RC_ERR;

    MonitorInfo found[16];
    int n = 0;
    Com<IDXGIAdapter1> adapter;
    for (UINT a = 0; n < 16 && SUCCEEDED(factory->EnumAdapters1(a, adapter.put())); a++) {
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
            m.pub.x = m.rect.left;
            m.pub.y = m.rect.top;
            m.pub.width = (uint32_t)(m.rect.right - m.rect.left);
            m.pub.height = (uint32_t)(m.rect.bottom - m.rect.top);
            m.pub.scale = monitor_scale(d.Monitor);
            MONITORINFOEXW mi;
            mi.cbSize = sizeof(mi);
            if (GetMonitorInfoW(d.Monitor, &mi)) m.pub.primary = (mi.dwFlags & MONITORINFOF_PRIMARY) ? 1 : 0;
            wide_to_utf8(d.DeviceName, m.pub.name, sizeof(m.pub.name));
            n++;
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

RECT virtual_desktop() {
    RECT r;
    r.left = GetSystemMetrics(SM_XVIRTUALSCREEN);
    r.top = GetSystemMetrics(SM_YVIRTUALSCREEN);
    r.right = r.left + GetSystemMetrics(SM_CXVIRTUALSCREEN);
    r.bottom = r.top + GetSystemMetrics(SM_CYVIRTUALSCREEN);
    return r;
}

} // namespace rcw

// Media Foundation loaded at runtime so remote-agent.exe still starts on
// Windows "N" editions without the Media Feature Pack (capture then fails
// with RC_ERR_UNSUPPORTED instead of the process failing to load).
#include "rc_win.h"

#include <mfapi.h>
#include <mfidl.h>

namespace rcw {

static MfApi g_mf;
static INIT_ONCE g_mf_once = INIT_ONCE_STATIC_INIT;

template <class T>
static bool sym(HMODULE m, const char *name, T *out) {
    *out = (T)(void *)GetProcAddress(m, name);
    return *out != nullptr;
}

static BOOL CALLBACK load_mf(PINIT_ONCE, PVOID, PVOID *) {
    HMODULE m = LoadLibraryExW(L"mfplat.dll", nullptr, LOAD_LIBRARY_SEARCH_SYSTEM32);
    if (!m) {
        logf("mf: mfplat.dll not available (Windows N without Media Feature Pack?)");
        return TRUE;
    }
    g_mf.ok = sym(m, "MFStartup", &g_mf.Startup) && sym(m, "MFShutdown", &g_mf.Shutdown) &&
              sym(m, "MFCreateMediaType", &g_mf.CreateMediaType) && sym(m, "MFCreateSample", &g_mf.CreateSample) &&
              sym(m, "MFCreateMemoryBuffer", &g_mf.CreateMemoryBuffer) &&
              sym(m, "MFCreateDXGISurfaceBuffer", &g_mf.CreateDXGISurfaceBuffer) &&
              sym(m, "MFCreateDXGIDeviceManager", &g_mf.CreateDXGIDeviceManager) &&
              sym(m, "MFTEnumEx", &g_mf.TEnumEx);
    if (!g_mf.ok) logf("mf: mfplat.dll is missing exports");
    return TRUE;
}

const MfApi &mf() {
    InitOnceExecuteOnce(&g_mf_once, load_mf, nullptr, nullptr);
    return g_mf;
}

bool mf_startup() {
    const MfApi &a = mf();
    return a.ok && SUCCEEDED(a.Startup(MF_VERSION, MFSTARTUP_NOSOCKET));
}

void mf_shutdown() {
    const MfApi &a = mf();
    if (a.ok) a.Shutdown();
}

} // namespace rcw

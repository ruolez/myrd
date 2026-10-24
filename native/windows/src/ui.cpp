// Device-local session UI: the red border overlay (layered, topmost,
// click-through, excluded from capture) and the blocking consent prompt.
#include "rc_win.h"

#include <stdio.h>
#include <stdlib.h>

#ifndef WDA_EXCLUDEFROMCAPTURE
#define WDA_EXCLUDEFROMCAPTURE 0x00000011
#endif

namespace rcw {

// ================================================================ indicator

static const COLORREF kKeyColor = RGB(255, 0, 255);
static const COLORREF kBorderColor = RGB(217, 48, 37); // Google red
static const int kBorder = 6;
static const UINT WM_RC_SHOW = WM_APP + 1;
static const UINT WM_RC_HIDE = WM_APP + 2;

static Lock g_ind_lock;
static HANDLE g_ind_thread = nullptr;
static DWORD g_ind_tid = 0;
static HANDLE g_ind_ready = nullptr;

static HWND g_borders[16];
static int g_border_count = 0;

static LRESULT CALLBACK border_proc(HWND h, UINT m, WPARAM w, LPARAM l) {
    if (m == WM_PAINT) {
        PAINTSTRUCT ps;
        HDC dc = BeginPaint(h, &ps);
        RECT r;
        GetClientRect(h, &r);
        HBRUSH key = CreateSolidBrush(kKeyColor);
        FillRect(dc, &r, key);
        DeleteObject(key);
        HBRUSH red = CreateSolidBrush(kBorderColor);
        RECT e;
        e = r; e.bottom = r.top + kBorder; FillRect(dc, &e, red);
        e = r; e.top = r.bottom - kBorder; FillRect(dc, &e, red);
        e = r; e.right = r.left + kBorder; FillRect(dc, &e, red);
        e = r; e.left = r.right - kBorder; FillRect(dc, &e, red);
        DeleteObject(red);
        EndPaint(h, &ps);
        return 0;
    }
    if (m == WM_NCHITTEST) return HTTRANSPARENT;
    return DefWindowProcW(h, m, w, l);
}

static BOOL CALLBACK add_border(HMONITOR mon, HDC, LPRECT rc, LPARAM) {
    if (g_border_count >= 16) return FALSE;
    HWND h = CreateWindowExW(WS_EX_LAYERED | WS_EX_TRANSPARENT | WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE,
                             L"RemotectlBorder", L"Remote session", WS_POPUP, rc->left, rc->top,
                             rc->right - rc->left, rc->bottom - rc->top, nullptr, nullptr,
                             GetModuleHandleW(nullptr), nullptr);
    (void)mon;
    if (!h) return TRUE;
    SetLayeredWindowAttributes(h, kKeyColor, 0, LWA_COLORKEY);
    // Keep the border out of the stream so the controller does not see it.
    // Earlier builds (Server 2019) take the value as WDA_MONITOR, which
    // captures this full-screen window as black over the whole desktop.
    if (os_excludes_from_capture()) SetWindowDisplayAffinity(h, WDA_EXCLUDEFROMCAPTURE);
    ShowWindow(h, SW_SHOWNOACTIVATE);
    g_borders[g_border_count++] = h;
    return TRUE;
}

static void destroy_borders() {
    for (int i = 0; i < g_border_count; i++) DestroyWindow(g_borders[i]);
    g_border_count = 0;
}

static void create_borders() {
    destroy_borders();
    EnumDisplayMonitors(nullptr, nullptr, add_border, 0);
}

static DWORD WINAPI indicator_thread(void *) {
    WNDCLASSW wc = {};
    wc.lpfnWndProc = border_proc;
    wc.hInstance = GetModuleHandleW(nullptr);
    wc.lpszClassName = L"RemotectlBorder";
    wc.hCursor = LoadCursor(nullptr, IDC_ARROW);
    RegisterClassW(&wc);
    MSG msg;
    PeekMessageW(&msg, nullptr, 0, 0, PM_NOREMOVE); // create the queue
    SetEvent(g_ind_ready);
    bool on = false;
    UINT_PTR timer = SetTimer(nullptr, 0, 1000, nullptr);
    while (GetMessageW(&msg, nullptr, 0, 0) > 0) {
        if (msg.message == WM_RC_SHOW) {
            on = true;
            create_borders();
        } else if (msg.message == WM_RC_HIDE) {
            on = false;
            destroy_borders();
        } else if (msg.message == WM_TIMER && msg.hwnd == nullptr) {
            if (on) {
                // Re-assert topmost; rebuild when the monitor layout changed.
                int n = 0;
                EnumDisplayMonitors(nullptr, nullptr,
                                    [](HMONITOR, HDC, LPRECT, LPARAM p) -> BOOL { (*(int *)p)++; return TRUE; },
                                    (LPARAM)&n);
                if (n != g_border_count) create_borders();
                for (int i = 0; i < g_border_count; i++)
                    SetWindowPos(g_borders[i], HWND_TOPMOST, 0, 0, 0, 0,
                                 SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE | SWP_SHOWWINDOW);
            }
        } else {
            TranslateMessage(&msg);
            DispatchMessageW(&msg);
        }
    }
    KillTimer(nullptr, timer);
    destroy_borders();
    return 0;
}

void indicator_show(bool on) {
    Guard g(g_ind_lock);
    if (!g_ind_thread) {
        if (!on) return;
        g_ind_ready = CreateEventW(nullptr, TRUE, FALSE, nullptr);
        g_ind_thread = CreateThread(nullptr, 0, indicator_thread, nullptr, 0, &g_ind_tid);
        if (!g_ind_thread) return;
        WaitForSingleObject(g_ind_ready, 2000);
    }
    PostThreadMessageW(g_ind_tid, on ? WM_RC_SHOW : WM_RC_HIDE, 0, 0);
}

// ================================================================ consent

enum { ID_DENY = 100, ID_ONCE = 101, ID_REMEMBER = 102 };

struct ConsentCtx {
    wchar_t *title;
    wchar_t *message;
    int remaining;
    int result;
    HWND countdown;
};

static void update_countdown(ConsentCtx *c) {
    wchar_t buf[96];
    swprintf(buf, 96, L"Denied automatically in %d s", c->remaining);
    SetWindowTextW(c->countdown, buf);
}

static LRESULT CALLBACK consent_proc(HWND h, UINT m, WPARAM w, LPARAM l) {
    ConsentCtx *c = (ConsentCtx *)GetWindowLongPtrW(h, GWLP_USERDATA);
    switch (m) {
    case WM_CREATE: {
        CREATESTRUCTW *cs = (CREATESTRUCTW *)l;
        c = (ConsentCtx *)cs->lpCreateParams;
        SetWindowLongPtrW(h, GWLP_USERDATA, (LONG_PTR)c);
        NONCLIENTMETRICSW ncm;
        ncm.cbSize = sizeof(ncm);
        SystemParametersInfoW(SPI_GETNONCLIENTMETRICS, sizeof(ncm), &ncm, 0);
        HFONT font = CreateFontIndirectW(&ncm.lfMessageFont);
        HINSTANCE hi = GetModuleHandleW(nullptr);
        HWND msg = CreateWindowExW(0, L"STATIC", c->message, WS_CHILD | WS_VISIBLE | SS_LEFT, 20, 18, 460, 90, h,
                                   nullptr, hi, nullptr);
        c->countdown = CreateWindowExW(0, L"STATIC", L"", WS_CHILD | WS_VISIBLE | SS_LEFT, 20, 114, 460, 20, h,
                                       nullptr, hi, nullptr);
        HWND b1 = CreateWindowExW(0, L"BUTTON", L"Deny", WS_CHILD | WS_VISIBLE | WS_TABSTOP | BS_DEFPUSHBUTTON, 20,
                                  146, 120, 30, h, (HMENU)(INT_PTR)ID_DENY, hi, nullptr);
        HWND b2 = CreateWindowExW(0, L"BUTTON", L"Allow once", WS_CHILD | WS_VISIBLE | WS_TABSTOP, 190, 146, 120,
                                  30, h, (HMENU)(INT_PTR)ID_ONCE, hi, nullptr);
        HWND b3 = CreateWindowExW(0, L"BUTTON", L"Allow and remember", WS_CHILD | WS_VISIBLE | WS_TABSTOP, 330, 146,
                                  150, 30, h, (HMENU)(INT_PTR)ID_REMEMBER, hi, nullptr);
        HWND all[] = {msg, c->countdown, b1, b2, b3};
        for (HWND x : all) SendMessageW(x, WM_SETFONT, (WPARAM)font, TRUE);
        update_countdown(c);
        SetTimer(h, 1, 1000, nullptr);
        SetFocus(b1);
        return 0;
    }
    case WM_TIMER:
        if (--c->remaining <= 0) {
            c->result = 0;
            DestroyWindow(h);
        } else {
            update_countdown(c);
        }
        return 0;
    case WM_COMMAND:
        switch (LOWORD(w)) {
        case ID_DENY: c->result = 0; DestroyWindow(h); return 0;
        case ID_ONCE: c->result = 1; DestroyWindow(h); return 0;
        case ID_REMEMBER: c->result = 2; DestroyWindow(h); return 0;
        }
        break;
    case WM_CLOSE:
        c->result = 0;
        DestroyWindow(h);
        return 0;
    case WM_DESTROY:
        PostQuitMessage(0);
        return 0;
    }
    return DefWindowProcW(h, m, w, l);
}

static DWORD WINAPI consent_thread(void *p) {
    ConsentCtx *c = (ConsentCtx *)p;
    // Show the prompt where the owner is looking, including the lock screen.
    attach_input_desktop();
    static bool registered = false;
    if (!registered) {
        WNDCLASSW wc = {};
        wc.lpfnWndProc = consent_proc;
        wc.hInstance = GetModuleHandleW(nullptr);
        wc.lpszClassName = L"RemotectlConsent";
        wc.hCursor = LoadCursor(nullptr, IDC_ARROW);
        wc.hbrBackground = (HBRUSH)(COLOR_BTNFACE + 1);
        wc.hIcon = LoadIcon(nullptr, IDI_WARNING);
        RegisterClassW(&wc);
        registered = true;
    }
    RECT wa;
    SystemParametersInfoW(SPI_GETWORKAREA, 0, &wa, 0);
    int w = 520, h = 230;
    int x = wa.left + (wa.right - wa.left - w) / 2;
    int y = wa.top + (wa.bottom - wa.top - h) / 2;
    HWND win = CreateWindowExW(WS_EX_TOPMOST | WS_EX_DLGMODALFRAME, L"RemotectlConsent", c->title,
                               WS_POPUP | WS_CAPTION | WS_SYSMENU, x, y, w, h, nullptr, nullptr,
                               GetModuleHandleW(nullptr), c);
    if (!win) {
        c->result = -1;
        return 0;
    }
    ShowWindow(win, SW_SHOWNORMAL);
    SetForegroundWindow(win);
    FlashWindow(win, TRUE);
    MSG msg;
    while (GetMessageW(&msg, nullptr, 0, 0) > 0) {
        if (!IsDialogMessageW(win, &msg)) {
            TranslateMessage(&msg);
            DispatchMessageW(&msg);
        }
    }
    return 0;
}

int consent_prompt(const char *title, const char *message, int timeout_s) {
    ConsentCtx c = {};
    c.title = utf8_to_wide(title ? title : "Remote session", -1);
    c.message = utf8_to_wide(message ? message : "", -1);
    c.remaining = timeout_s > 0 ? timeout_s : 60;
    c.result = 0;
    HANDLE t = CreateThread(nullptr, 0, consent_thread, &c, 0, nullptr);
    if (t) {
        WaitForSingleObject(t, INFINITE);
        CloseHandle(t);
    } else {
        c.result = -1;
    }
    free(c.title);
    free(c.message);
    return c.result < 0 ? RC_ERR : c.result;
}

} // namespace rcw

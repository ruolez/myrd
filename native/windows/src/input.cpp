// Input injection with SendInput. The calling thread is moved to the input
// desktop first so UAC prompts and the lock screen stay controllable (the
// helper runs as SYSTEM in the console session).
#include "rc_win.h"

#include <stdlib.h>

namespace rcw {

// Pause has no plain set-1 scancode (E1 1D 45); the keymap sends this
// sentinel and we inject VK_PAUSE instead.
static const uint32_t kPauseSentinel = 0xE11D45;

static const double kPixelsPerNotch = 100.0; // Chrome's deltaY per wheel notch on Windows

static double g_wheel_acc_x = 0, g_wheel_acc_y = 0;

static bool send(INPUT *in, UINT n) {
    attach_input_desktop();
    return SendInput(n, in, sizeof(INPUT)) == n;
}

static int mouse_move_abs(const rc_input_event *ev) {
    MonitorInfo m;
    if (!find_monitor(ev->monitor_id, &m)) return RC_ERR;
    LONG mw = m.rect.right - m.rect.left;
    LONG mh = m.rect.bottom - m.rect.top;
    LONG px = m.rect.left + (LONG)((int64_t)ev->x * (mw - 1) / 65535);
    LONG py = m.rect.top + (LONG)((int64_t)ev->y * (mh - 1) / 65535);
    RECT v = virtual_desktop();
    LONG vw = v.right - v.left, vh = v.bottom - v.top;
    INPUT in = {};
    in.type = INPUT_MOUSE;
    in.mi.dx = vw > 1 ? (LONG)((int64_t)(px - v.left) * 65535 / (vw - 1)) : 0;
    in.mi.dy = vh > 1 ? (LONG)((int64_t)(py - v.top) * 65535 / (vh - 1)) : 0;
    in.mi.dwFlags = MOUSEEVENTF_MOVE | MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK;
    return send(&in, 1) ? RC_OK : RC_ERR;
}

static int mouse_button(const rc_input_event *ev) {
    INPUT in = {};
    in.type = INPUT_MOUSE;
    bool d = ev->down != 0;
    switch (ev->button) {
    case RC_BTN_LEFT: in.mi.dwFlags = d ? MOUSEEVENTF_LEFTDOWN : MOUSEEVENTF_LEFTUP; break;
    case RC_BTN_RIGHT: in.mi.dwFlags = d ? MOUSEEVENTF_RIGHTDOWN : MOUSEEVENTF_RIGHTUP; break;
    case RC_BTN_MIDDLE: in.mi.dwFlags = d ? MOUSEEVENTF_MIDDLEDOWN : MOUSEEVENTF_MIDDLEUP; break;
    case RC_BTN_BACK:
        in.mi.dwFlags = d ? MOUSEEVENTF_XDOWN : MOUSEEVENTF_XUP;
        in.mi.mouseData = XBUTTON1;
        break;
    case RC_BTN_FORWARD:
        in.mi.dwFlags = d ? MOUSEEVENTF_XDOWN : MOUSEEVENTF_XUP;
        in.mi.mouseData = XBUTTON2;
        break;
    default: return RC_ERR;
    }
    return send(&in, 1) ? RC_OK : RC_ERR;
}

static int mouse_wheel(const rc_input_event *ev) {
    // DOM sign: dy > 0 scrolls content down = negative wheel delta on Windows.
    g_wheel_acc_y += -ev->dy * WHEEL_DELTA / kPixelsPerNotch;
    g_wheel_acc_x += ev->dx * WHEEL_DELTA / kPixelsPerNotch;
    INPUT in[2] = {};
    UINT n = 0;
    LONG vy = (LONG)g_wheel_acc_y, vx = (LONG)g_wheel_acc_x;
    if (vy != 0) {
        g_wheel_acc_y -= vy;
        in[n].type = INPUT_MOUSE;
        in[n].mi.dwFlags = MOUSEEVENTF_WHEEL;
        in[n].mi.mouseData = (DWORD)vy;
        n++;
    }
    if (vx != 0) {
        g_wheel_acc_x -= vx;
        in[n].type = INPUT_MOUSE;
        in[n].mi.dwFlags = MOUSEEVENTF_HWHEEL;
        in[n].mi.mouseData = (DWORD)vx;
        n++;
    }
    if (n == 0) return RC_OK;
    return send(in, n) ? RC_OK : RC_ERR;
}

static int mouse_move_rel(const rc_input_event *ev) {
    INPUT in = {};
    in.type = INPUT_MOUSE;
    in.mi.dx = ev->dx;
    in.mi.dy = ev->dy;
    in.mi.dwFlags = MOUSEEVENTF_MOVE;
    return send(&in, 1) ? RC_OK : RC_ERR;
}

static int key(const rc_input_event *ev) {
    INPUT in = {};
    in.type = INPUT_KEYBOARD;
    if (ev->keycode == kPauseSentinel) {
        in.ki.wVk = VK_PAUSE;
        in.ki.dwFlags = ev->down ? 0 : KEYEVENTF_KEYUP;
        return send(&in, 1) ? RC_OK : RC_ERR;
    }
    if (ev->keycode == 0 || ev->keycode > 0xFF) return RC_ERR;
    in.ki.wScan = (WORD)ev->keycode;
    in.ki.dwFlags = KEYEVENTF_SCANCODE;
    if (ev->flags & RC_KEYFLAG_EXTENDED) in.ki.dwFlags |= KEYEVENTF_EXTENDEDKEY;
    if (!ev->down) in.ki.dwFlags |= KEYEVENTF_KEYUP;
    return send(&in, 1) ? RC_OK : RC_ERR;
}

int input_inject(const rc_input_event *ev) {
    if (!ev) return RC_ERR;
    switch (ev->type) {
    case RC_EV_MOUSE_MOVE: return mouse_move_abs(ev);
    case RC_EV_MOUSE_BUTTON: return mouse_button(ev);
    case RC_EV_MOUSE_WHEEL: return mouse_wheel(ev);
    case RC_EV_KEY: return key(ev);
    case RC_EV_MOUSE_MOVE_REL: return mouse_move_rel(ev);
    default: return RC_ERR_UNSUPPORTED;
    }
}

int input_text(const char *utf8, int32_t len) {
    wchar_t *w = utf8_to_wide(utf8, len);
    if (!w) return RC_ERR;
    size_t n = wcslen(w);
    INPUT *in = (INPUT *)calloc(n * 2 + 2, sizeof(INPUT));
    if (!in) {
        free(w);
        return RC_ERR;
    }
    UINT k = 0;
    for (size_t i = 0; i < n; i++) {
        wchar_t ch = w[i];
        if (ch == L'\r') continue;
        if (ch == L'\n') {
            in[k].type = INPUT_KEYBOARD;
            in[k].ki.wVk = VK_RETURN;
            k++;
            in[k].type = INPUT_KEYBOARD;
            in[k].ki.wVk = VK_RETURN;
            in[k].ki.dwFlags = KEYEVENTF_KEYUP;
            k++;
            continue;
        }
        // Surrogate pairs go through as two KEYEVENTF_UNICODE units.
        in[k].type = INPUT_KEYBOARD;
        in[k].ki.wScan = ch;
        in[k].ki.dwFlags = KEYEVENTF_UNICODE;
        k++;
        in[k].type = INPUT_KEYBOARD;
        in[k].ki.wScan = ch;
        in[k].ki.dwFlags = KEYEVENTF_UNICODE | KEYEVENTF_KEYUP;
        k++;
    }
    bool ok = k == 0 || send(in, k);
    free(in);
    free(w);
    return ok ? RC_OK : RC_ERR;
}

// ---- clipboard ----

static bool open_clipboard() {
    attach_input_desktop();
    for (int i = 0; i < 10; i++) {
        if (OpenClipboard(nullptr)) return true;
        Sleep(10);
    }
    return false;
}

int clipboard_get_text(char **out, int32_t *len) {
    *out = nullptr;
    *len = 0;
    if (!open_clipboard()) return RC_ERR;
    int rc = RC_ERR;
    HANDLE h = GetClipboardData(CF_UNICODETEXT);
    if (h) {
        const wchar_t *w = (const wchar_t *)GlobalLock(h);
        if (w) {
            int n = WideCharToMultiByte(CP_UTF8, 0, w, -1, nullptr, 0, nullptr, nullptr);
            char *s = (char *)malloc(n > 0 ? (size_t)n : 1);
            if (s) {
                if (n > 0) WideCharToMultiByte(CP_UTF8, 0, w, -1, s, n, nullptr, nullptr);
                else s[0] = 0;
                *out = s;
                *len = n > 0 ? n - 1 : 0;
                rc = RC_OK;
            }
            GlobalUnlock(h);
        }
    } else {
        // Empty clipboard is not an error.
        char *s = (char *)malloc(1);
        if (s) {
            s[0] = 0;
            *out = s;
            rc = RC_OK;
        }
    }
    CloseClipboard();
    return rc;
}

int clipboard_set_text(const char *utf8, int32_t len) {
    wchar_t *w = utf8_to_wide(utf8, len);
    if (!w) return RC_ERR;
    size_t bytes = (wcslen(w) + 1) * sizeof(wchar_t);
    HGLOBAL g = GlobalAlloc(GMEM_MOVEABLE, bytes);
    if (!g) {
        free(w);
        return RC_ERR;
    }
    memcpy(GlobalLock(g), w, bytes);
    GlobalUnlock(g);
    free(w);
    if (!open_clipboard()) {
        GlobalFree(g);
        return RC_ERR;
    }
    EmptyClipboard();
    bool ok = SetClipboardData(CF_UNICODETEXT, g) != nullptr;
    CloseClipboard();
    if (!ok) GlobalFree(g);
    return ok ? RC_OK : RC_ERR;
}

} // namespace rcw

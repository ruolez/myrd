import { createBrowserRouter, Navigate, Outlet, RouterProvider, useLocation } from "react-router";
import { ErrorBox, FeedbackProvider, Loading } from "../components/ui";
import { AdminAudit } from "../pages/admin/AdminAudit";
import { AdminDevices } from "../pages/admin/AdminDevices";
import { AdminOverviewPage } from "../pages/admin/AdminOverview";
import { AdminReleases } from "../pages/admin/AdminReleases";
import { AdminSessions } from "../pages/admin/AdminSessions";
import { AdminSettingsPage } from "../pages/admin/AdminSettings";
import { AdminUsers } from "../pages/admin/AdminUsers";
import { Activity } from "../pages/Activity";
import { AddDevice } from "../pages/AddDevice";
import { DeviceDetail } from "../pages/DeviceDetail";
import { Devices } from "../pages/Devices";
import { Login } from "../pages/Login";
import { NotFound } from "../pages/NotFound";
import { SessionView } from "../pages/SessionView";
import { Settings } from "../pages/Settings";
import { Signup } from "../pages/Signup";
import { AuthProvider, useAuth } from "./auth";
import { Layout } from "./Layout";
import { ThemeProvider } from "./theme";

function RequireAuth() {
  const { user, loading, bootError } = useAuth();
  const loc = useLocation();
  if (loading) return <Loading label="Starting…" />;
  if (!user) {
    if (bootError)
      return (
        <div className="mx-auto max-w-md p-8">
          <ErrorBox error={bootError} onRetry={() => location.reload()} />
        </div>
      );
    return <Navigate to={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`} replace />;
  }
  return <Outlet />;
}

function RequireAdmin() {
  const { user } = useAuth();
  if (!user?.is_admin) return <Navigate to="/devices" replace />;
  return <Outlet />;
}

function Root() {
  return (
    <ThemeProvider>
      <AuthProvider>
        <FeedbackProvider>
          <Outlet />
        </FeedbackProvider>
      </AuthProvider>
    </ThemeProvider>
  );
}

const router = createBrowserRouter([
  {
    element: <Root />,
    children: [
      { path: "/login", element: <Login /> },
      { path: "/signup", element: <Signup /> },
      {
        element: <RequireAuth />,
        children: [
          { path: "/session/:deviceId", element: <SessionView /> },
          {
            element: <Layout />,
            children: [
              { path: "/", element: <Navigate to="/devices" replace /> },
              { path: "/devices", element: <Devices /> },
              { path: "/devices/add", element: <AddDevice /> },
              { path: "/devices/:id", element: <DeviceDetail /> },
              { path: "/activity", element: <Activity /> },
              { path: "/settings", element: <Settings /> },
              {
                path: "/admin",
                element: <RequireAdmin />,
                children: [
                  { index: true, element: <AdminOverviewPage /> },
                  { path: "users", element: <AdminUsers /> },
                  { path: "devices", element: <AdminDevices /> },
                  { path: "sessions", element: <AdminSessions /> },
                  { path: "audit", element: <AdminAudit /> },
                  { path: "releases", element: <AdminReleases /> },
                  { path: "settings", element: <AdminSettingsPage /> },
                ],
              },
              { path: "*", element: <NotFound /> },
            ],
          },
        ],
      },
    ],
  },
]);

export function App() {
  return <RouterProvider router={router} />;
}

import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import { QueryCache, QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { createRootRoute, createRouter, RouterProvider, useRouterState } from "@tanstack/react-router";
import { AdministrationPage, ApiResponseError, AppShell, AuthProvider, Brand, clearAuthSession, createApiClient, EndpointTable, getPage, LoginForm, PageErrorBoundary, PasswordSetupForm, portalSchema, ProfilePage, ResetRequestForm, SystemConsole, useAuth, type PortalPage } from "@ddp/ui";
import branding from "virtual:ddp-branding";
import "./styles.css";

document.title = branding.display_name;

import.meta.glob(["./routes/**/*.tsx", "!./routes/**/*.test.tsx"], { eager: true });

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError: (error) => {
    if (error instanceof ApiResponseError && error.status === 401) queueMicrotask(() => {
      clearAuthSession(queryClient);
    });
  } }),
  defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } },
});

function message(error: unknown) {
  return error instanceof Error ? error.message : "Something went wrong. Try again.";
}

function Loading({ label = "Loading workspace" }: { label?: string }) {
  return <div className="loading" role="status"><span aria-hidden="true" /><span>{label}…</span></div>;
}

function LoginScreen({ returnTo }: { returnTo: string }) {
  return <main className="login-page">
    <section className="login-brand" aria-labelledby="login-title">
      <a className="brand" href="/" aria-label={`${branding.display_name} home`}><Brand displayName={branding.display_name} logo={branding.logo} tagline="Client data systems" /></a>
      <div>
        <h1 id="login-title">Your operation, in one clear view.</h1>
        <p>Secure access to the reporting and workflows your team uses every day.</p>
      </div>
      <small className="login-footnote">Protected by your organization’s DDP workspace.</small>
    </section>
    <section className="login-panel" aria-labelledby="form-title">
      <div className="login-panel-inner">
        <h2 id="form-title">Sign in</h2>
        <p>Use your workspace credentials to continue.</p>
        <LoginForm onSuccess={() => { if (returnTo !== "/login") window.location.replace(returnTo); }} />
        <a className="account-link" href="/forgot-password">Forgot your password?</a>
      </div>
    </section>
  </main>;
}

function AccountScreen({ kind }: { kind: "forgot" | "password" }) {
  const password = kind === "password";
  return <main className="login-page">
    <section className="login-brand" aria-labelledby="account-title">
      <a className="brand" href="/" aria-label={`${branding.display_name} home`}><Brand displayName={branding.display_name} logo={branding.logo} tagline="Client data systems" /></a>
      <div><h1 id="account-title">{password ? "Set a new password." : "Get back into your workspace."}</h1><p>{password ? "Choose a password you’ll remember. Your other sessions will be signed out." : "We’ll send a secure link if an account matches your email."}</p></div>
      <small className="login-footnote">Protected by your organization’s DDP workspace.</small>
    </section>
    <section className="login-panel" aria-labelledby="form-title"><div className="login-panel-inner">
      <h2 id="form-title">{password ? "Set password" : "Forgot password?"}</h2>
      <p>{password ? "Use at least 8 UTF-8 bytes and no more than 128." : "Enter your work email to request a reset link."}</p>
      {password ? <PasswordSetupForm /> : <ResetRequestForm />}
      <a className="account-link" href="/login">Back to sign in</a>
    </div></section>
  </main>;
}

function Home({ pages }: { pages: PortalPage[] }) {
  return <section aria-labelledby="page-title">
    <div className="page-heading"><div><h1 id="page-title">Workspace</h1><p>Choose a page to view the latest available data.</p></div></div>
    <div className="page-list">{pages.map((page) => <a href={page.path} key={page.path}><strong>{page.label}</strong><span>Open page</span></a>)}</div>
  </section>;
}

function PortalApp() {
  const path = useRouterState({ select: (state) => state.location.pathname });
  const { session, isPending, error: sessionError, logout, isLoggingOut } = useAuth();
  const [logoutError, setLogoutError] = useState("");
  const portalQuery = useQuery({ queryKey: ["portal"], queryFn: () => createApiClient().request("/portal", portalSchema), enabled: Boolean(session) });
  if (isPending) return <Loading />;
  if (sessionError) return <main className="fatal-error" role="alert"><strong>Couldn’t reach your workspace.</strong><span>{message(sessionError)}</span><button type="button" onClick={() => window.location.reload()}>Try again</button></main>;
  if (!session) return <LoginScreen returnTo={path} />;
  if (portalQuery.isPending) return <Loading />;
  if (portalQuery.error || !portalQuery.data) return <main className="fatal-error" role="alert"><strong>Couldn’t load your workspace.</strong><span>{message(portalQuery.error)}</span><button type="button" onClick={() => portalQuery.refetch()}>Try again</button></main>;

  const portal = portalQuery.data;
  const page = portal.pages.find((candidate) => candidate.path === path);
  let content;
  const customPage = page?.kind === "custom" ? getPage(page.id) : undefined;
  if (path === "/profile") content = <ProfilePage />;
  else if (path === "/admin/users" && (session.user.admin || session.user.permissions.includes("users.manage"))) content = <AdministrationPage />;
  else if (path === "/login") content = <Home pages={portal.pages} />;
  else if (page?.kind === "custom" && customPage) {
    const Page = customPage;
    content = <PageErrorBoundary key={page.id}><Page /></PageErrorBoundary>;
  }
  else if (path === "/" && !page) content = <Home pages={portal.pages} />;
  else if (page?.kind === "table" && page.endpoint && page.columns) content = <EndpointTable key={JSON.stringify(page)} page={page} />;
  else if (page?.kind === "system" && session.user.admin) content = <SystemConsole label={page.label} />;
  else content = <section className="not-found"><h1>Page unavailable</h1><p>This page is not part of your workspace.</p><a href="/">Return to workspace</a></section>;

  return <AppShell portal={portal} session={session} logo={branding.logo} activePath={path} onLogout={() => {
    setLogoutError("");
    logout().catch((error: unknown) => setLogoutError(message(error)));
  }} logoutPending={isLoggingOut}>
    {logoutError && <div className="logout-error" role="alert">Sign out failed. {logoutError}</div>}
    {content}
  </AppShell>;
}

function Root() {
  const path = window.location.pathname;
  if (path === "/forgot-password") return <QueryClientProvider client={queryClient}><AccountScreen kind="forgot" /></QueryClientProvider>;
  if (path === "/welcome" || path === "/reset-password") return <QueryClientProvider client={queryClient}><AccountScreen kind="password" /></QueryClientProvider>;
  return <QueryClientProvider client={queryClient}><AuthProvider><PortalApp /></AuthProvider></QueryClientProvider>;
}

const rootRoute = createRootRoute({ component: Root });
const router = createRouter({ routeTree: rootRoute });
declare module "@tanstack/react-router" { interface Register { router: typeof router } }

createRoot(document.getElementById("root")!).render(<StrictMode><RouterProvider router={router} /></StrictMode>);

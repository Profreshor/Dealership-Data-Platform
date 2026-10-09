import { createContext, useContext, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { z } from "zod";
import { ApiResponseError, createApiClient, sessionSchema, type Session } from "./api";

export const authSessionKey = ["auth", "session"] as const;
// Keep the observed session query alive while removing private data. Removing it
// during concurrent 401 responses can remount observers and refetch indefinitely.
export function clearAuthSession(queryClient: QueryClient) {
  void queryClient.cancelQueries();
  queryClient.setQueryData(authSessionKey, null);
  queryClient.getMutationCache().clear();
  queryClient.removeQueries({ predicate: (query) => query.queryKey.length !== 2 || query.queryKey[0] !== "auth" || query.queryKey[1] !== "session" });
}

type Credentials = { email: string; password: string };
type AuthState = {
  session: Session | null;
  isPending: boolean;
  error: Error | null;
  login: (credentials: Credentials) => Promise<Session>;
  logout: () => Promise<void>;
  isLoggingIn: boolean;
  isLoggingOut: boolean;
};

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const sessionQuery = useQuery({
    queryKey: authSessionKey,
    queryFn: async () => {
      try {
        return await createApiClient().request("/auth/session", sessionSchema);
      } catch (error) {
        if (error instanceof ApiResponseError && error.status === 401) return null;
        throw error;
      }
    },
    retry: false,
    staleTime: 30_000,
  });
  const loginMutation = useMutation({
    mutationFn: (credentials: Credentials) => createApiClient().request("/auth/login", sessionSchema, { method: "POST", body: JSON.stringify(credentials) }),
    onSuccess: (session) => queryClient.setQueryData(authSessionKey, session),
  });
  const logoutMutation = useMutation({
    mutationFn: () => createApiClient({ csrfToken: sessionQuery.data?.csrf_token }).request("/auth/logout", z.null(), { method: "POST" }),
    onSuccess: () => {
      clearAuthSession(queryClient);
    },
  });
  return <AuthContext.Provider value={{
    session: sessionQuery.data ?? null,
    isPending: sessionQuery.isPending,
    error: sessionQuery.error,
    login: loginMutation.mutateAsync,
    logout: async () => { await logoutMutation.mutateAsync(); },
    isLoggingIn: loginMutation.isPending,
    isLoggingOut: logoutMutation.isPending,
  }}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const auth = useContext(AuthContext);
  if (!auth) throw new Error("useAuth must be used within AuthProvider");
  return auth;
}

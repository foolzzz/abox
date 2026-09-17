import { createContext, useContext, type ReactNode } from "react";
import type { CurrentUser, Meta, OrganizationRole } from "../api/types";

const ROLE_LEVEL: Record<OrganizationRole, number> = {
  viewer: 1,
  operator: 2,
  admin: 3,
  owner: 4
};

interface AccessContextValue {
  meta?: Meta;
  currentUser?: CurrentUser;
  loading: boolean;
  error: unknown;
}

const AccessContext = createContext<AccessContextValue>({ loading: true, error: undefined });

export function AccessProvider({
  meta,
  loading,
  error,
  children
}: AccessContextValue & { children: ReactNode }) {
  return (
    <AccessContext.Provider value={{ meta, currentUser: meta?.currentUser, loading, error }}>
      {children}
    </AccessContext.Provider>
  );
}

export function useAccess(): AccessContextValue {
  return useContext(AccessContext);
}

export function roleAtLeast(role: OrganizationRole | string | undefined, minimum: OrganizationRole): boolean {
  return Boolean(role && (ROLE_LEVEL[role as OrganizationRole] ?? 0) >= ROLE_LEVEL[minimum]);
}

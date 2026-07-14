"use client";

import { SessionProvider } from "next-auth/react";

// Client-side session context so `useSession()` works across the app.
export default function Providers({ children }: { children: React.ReactNode }) {
  return <SessionProvider>{children}</SessionProvider>;
}

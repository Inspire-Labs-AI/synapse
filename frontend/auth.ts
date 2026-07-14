// Auth.js (next-auth v5) configuration for Project Synapse.
//
// Providers are enabled conditionally:
//   - GitHub / Google  — active only when their OAuth client id + secret are set
//     in the environment (register an app and add the vars to enable).
//   - "local"          — a credentials provider that grants an instant local
//     developer session, so this local-first tool is fully usable WITHOUT
//     registering any external OAuth app.
//
// Sessions are JWT (no database adapter), which keeps the middleware edge-safe
// and is required by the credentials provider.
import NextAuth, { type NextAuthConfig } from "next-auth";
import GitHub from "next-auth/providers/github";
import Google from "next-auth/providers/google";
import Credentials from "next-auth/providers/credentials";

const providers: NextAuthConfig["providers"] = [
  Credentials({
    id: "local",
    name: "Local Developer",
    credentials: {},
    authorize: () => ({
      id: "local-dev",
      name: "Local Developer",
      email: "dev@localhost",
      image: "",
    }),
  }),
];

if (process.env.GITHUB_CLIENT_ID && process.env.GITHUB_CLIENT_SECRET) {
  providers.push(
    GitHub({
      clientId: process.env.GITHUB_CLIENT_ID,
      clientSecret: process.env.GITHUB_CLIENT_SECRET,
    }),
  );
}

if (process.env.GOOGLE_CLIENT_ID && process.env.GOOGLE_CLIENT_SECRET) {
  providers.push(
    Google({
      clientId: process.env.GOOGLE_CLIENT_ID,
      clientSecret: process.env.GOOGLE_CLIENT_SECRET,
    }),
  );
}

export const { handlers, auth, signIn, signOut } = NextAuth({
  providers,
  // Production MUST set AUTH_SECRET; the fallback keeps local-dev frictionless.
  secret: process.env.AUTH_SECRET || "dev-insecure-secret-change-me-in-prod",
  trustHost: true,
  session: { strategy: "jwt" },
  pages: { signIn: "/" },
});

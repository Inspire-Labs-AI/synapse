// Auth.js route handler — mounts GET/POST /api/auth/* (sign-in, callback,
// session, csrf, signout) from the central config in /auth.ts.
import { handlers } from "@/auth";

export const { GET, POST } = handlers;

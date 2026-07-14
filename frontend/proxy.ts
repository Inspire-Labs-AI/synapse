// Route protection (Next.js 16 "proxy" convention; formerly middleware): the
// workspace, docs, and architecture views require an active session.
// Unauthenticated visitors are redirected to the landing page to sign in.
import { auth } from "@/auth";
import { NextResponse } from "next/server";

export default auth((req) => {
  if (!req.auth) {
    return NextResponse.redirect(new URL("/", req.nextUrl.origin));
  }
  return NextResponse.next();
});

export const config = {
  matcher: ["/workspace/:path*", "/architecture/:path*", "/docs/:path*", "/prune/:path*"],
};

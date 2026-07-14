import Onboarding from "./components/Onboarding";

// Server component: reads which OAuth providers are configured (so the client
// can show real buttons vs. "not configured"), then hands off to the animated
// client onboarding controller.
export default function Landing() {
  const githubEnabled = Boolean(process.env.GITHUB_CLIENT_ID && process.env.GITHUB_CLIENT_SECRET);
  const googleEnabled = Boolean(process.env.GOOGLE_CLIENT_ID && process.env.GOOGLE_CLIENT_SECRET);
  return <Onboarding githubEnabled={githubEnabled} googleEnabled={googleEnabled} />;
}

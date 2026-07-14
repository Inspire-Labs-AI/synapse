# Security Policy

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, report them privately using GitHub's
[**Security Advisories**](../../security/advisories/new) ("Report a
vulnerability" on the repository's Security tab), or email the maintainers at
**security@project-synapse.dev** *(update this to a real contact before launch)*.

Please include, where possible:

- A description of the vulnerability and its impact
- Steps to reproduce (a proof of concept if you have one)
- Affected component (backend API, parser, ingestion, frontend, etc.)
- Any suggested remediation

We aim to acknowledge reports within a few days and will keep you updated on
progress. We ask that you give us a reasonable window to fix the issue before
any public disclosure.

## Scope & good-to-know

Synapse is designed as a **local-first, single-tenant** tool:

- The HTTP API and CORS are intentionally open for local development. If you
  expose the backend beyond `localhost`, you are responsible for putting it
  behind authentication and a network boundary.
- The "Continue as Local Developer" sign-in is a local convenience and must be
  disabled/replaced before any shared or public deployment.
- The engine can send your source code to third-party embedding/LLM providers
  when you configure a provider key. It runs fully offline (deterministic
  embeddings + a template responder) when no keys are set.

Never commit real API keys — only `.env.example` templates are tracked; real
`.env` / `.env.local` files are gitignored.

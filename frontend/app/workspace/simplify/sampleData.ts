import type { SynapseEdge, SynapseNode } from "./types";

// A small but representative repo graph. Positions are intentionally omitted —
// layoutGraph() derives nested positions + container footprints. Consumers are
// listed left (frontend) and providers right (backend) so edges flow forward.
// Parent nodes precede their children (React Flow's recommended ordering).
export const sampleNodes: SynapseNode[] = [
  // --- frontend/ ----------------------------------------------------------
  {
    id: "frontend",
    type: "group",
    position: { x: 0, y: 0 },
    data: { name: "frontend/", entityType: "directory", childCount: 0, expandedWidth: 0, expandedHeight: 0 },
  },
  {
    id: "app",
    type: "codeEntity",
    parentId: "frontend",
    position: { x: 0, y: 0 },
    data: {
      name: "App.tsx",
      entityType: "file",
      package: "react",
      language: "tsx",
      loc: 210,
      summary: "Root component; composes the router and data providers.",
    },
  },
  // An EXPANDABLE code entity — a file that owns a function-level subgraph.
  {
    id: "api",
    type: "codeEntity",
    parentId: "frontend",
    position: { x: 0, y: 0 },
    data: { name: "api.ts", entityType: "file", package: "fetch", language: "ts", loc: 64 },
  },
  {
    id: "fetchUser",
    type: "codeEntity",
    parentId: "api",
    position: { x: 0, y: 0 },
    data: {
      name: "fetchUser()",
      entityType: "function",
      language: "ts",
      loc: 18,
      summary: "GETs /users/:id and parses the JSON payload.",
    },
  },
  {
    id: "postLogin",
    type: "codeEntity",
    parentId: "api",
    position: { x: 0, y: 0 },
    data: {
      name: "postLogin()",
      entityType: "function",
      language: "ts",
      loc: 22,
      summary: "POSTs credentials, stores the returned session token.",
    },
  },

  // --- backend/ -----------------------------------------------------------
  {
    id: "backend",
    type: "group",
    position: { x: 0, y: 0 },
    data: { name: "backend/", entityType: "directory", childCount: 0, expandedWidth: 0, expandedHeight: 0 },
  },
  {
    id: "server",
    type: "codeEntity",
    parentId: "backend",
    position: { x: 0, y: 0 },
    data: {
      name: "server.ts",
      entityType: "endpoint",
      package: "http",
      language: "ts",
      loc: 240,
      summary: "Boots the HTTP API, mounts routers, wires the request pipeline.",
    },
  },
  {
    id: "db",
    type: "codeEntity",
    parentId: "backend",
    position: { x: 0, y: 0 },
    data: {
      name: "db.ts",
      entityType: "file",
      package: "pg",
      language: "ts",
      loc: 96,
      summary: "Postgres connection pool + typed query helpers.",
    },
  },
  // backend/auth (nested module)
  {
    id: "auth",
    type: "group",
    parentId: "backend",
    position: { x: 0, y: 0 },
    data: { name: "auth", entityType: "module", childCount: 0, expandedWidth: 0, expandedHeight: 0 },
  },
  {
    id: "session",
    type: "codeEntity",
    parentId: "auth",
    position: { x: 0, y: 0 },
    data: {
      name: "session.ts",
      entityType: "class",
      package: "jwt",
      language: "ts",
      loc: 130,
      summary: "Issues + verifies signed session tokens.",
    },
  },
  {
    id: "oauth",
    type: "codeEntity",
    parentId: "auth",
    position: { x: 0, y: 0 },
    data: { name: "oauth.ts", entityType: "file", package: "oauth2", language: "ts", loc: 88 },
  },
];

export const sampleEdges: SynapseEdge[] = [
  { id: "e1", source: "app", target: "api" },
  { id: "e2", source: "api", target: "server" },
  { id: "e3", source: "fetchUser", target: "server" },
  { id: "e4", source: "postLogin", target: "session" },
  { id: "e5", source: "server", target: "db" },
  { id: "e6", source: "server", target: "session" },
  { id: "e7", source: "oauth", target: "session" },
  { id: "e8", source: "app", target: "server" },
];

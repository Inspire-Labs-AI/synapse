// Project Synapse — TypeScript/JavaScript AST extractor.
//
// Invoked by the Go ingestion engine. Reads a JSON payload from stdin:
//   { "files": [ { "relPath": "src/a.ts", "source": "<file text>" }, ... ] }
// and writes a JSON result to stdout:
//   { "results": [ { "relPath", "imports": [...], "exports": [...], "endpoints": [...], "error"? } ] }
//
// Uses the real TypeScript compiler AST (ts.createSourceFile) — no type
// checker / tsconfig needed, which keeps it fast and dependency-light while
// still being a true parse (not regex tokenizing).

import ts from "typescript";

const HTTP_METHODS = new Set([
  "GET",
  "POST",
  "PUT",
  "DELETE",
  "PATCH",
  "HEAD",
  "OPTIONS",
]);

// Router/handler method names for Express / Fastify / Next custom servers.
const ROUTER_METHODS = new Set([
  "get",
  "post",
  "put",
  "delete",
  "patch",
  "options",
  "head",
  "all",
]);

function readStdin() {
  return new Promise((resolve, reject) => {
    let data = "";
    process.stdin.setEncoding("utf8");
    process.stdin.on("data", (chunk) => (data += chunk));
    process.stdin.on("end", () => resolve(data));
    process.stdin.on("error", reject);
  });
}

function scriptKind(relPath) {
  if (relPath.endsWith(".tsx")) return ts.ScriptKind.TSX;
  if (relPath.endsWith(".ts")) return ts.ScriptKind.TS;
  if (relPath.endsWith(".jsx")) return ts.ScriptKind.JSX;
  if (relPath.endsWith(".mjs") || relPath.endsWith(".cjs")) return ts.ScriptKind.JS;
  return ts.ScriptKind.JS;
}

function lineOf(sf, node) {
  return sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;
}

function endLineOf(sf, node) {
  return sf.getLineAndCharacterOfPosition(node.getEnd()).line + 1;
}

function modifierFlags(stmt) {
  const mods = ts.canHaveModifiers(stmt) ? ts.getModifiers(stmt) : undefined;
  return {
    exported: !!mods && mods.some((m) => m.kind === ts.SyntaxKind.ExportKeyword),
    isDefault: !!mods && mods.some((m) => m.kind === ts.SyntaxKind.DefaultKeyword),
  };
}

// --- Dendrite Callouts: detect structural idioms on a top-level statement -----

function nodeHasDecorators(node) {
  if (ts.canHaveDecorators && ts.canHaveDecorators(node)) {
    const d = ts.getDecorators(node);
    if (d && d.length) return true;
  }
  if (ts.isClassDeclaration(node) && node.members) {
    for (const m of node.members) {
      if (ts.canHaveDecorators(m)) {
        const d = ts.getDecorators(m);
        if (d && d.length) return true;
      }
    }
  }
  return false;
}

// A function-like that returns a function/arrow is a closure factory.
function returnsFunction(node) {
  let found = false;
  function walk(n) {
    if (found || !n) return;
    if (
      ts.isReturnStatement(n) &&
      n.expression &&
      (ts.isArrowFunction(n.expression) || ts.isFunctionExpression(n.expression))
    ) {
      found = true;
      return;
    }
    ts.forEachChild(n, walk);
  }
  if (node.body) walk(node.body);
  return found;
}

// detectPatterns returns the dendrite idioms found on a top-level statement.
function detectPatterns(stmt) {
  const p = [];
  if (nodeHasDecorators(stmt)) p.push("decorator");
  if (stmt.typeParameters && stmt.typeParameters.length) p.push("generic_wrapper");
  if (ts.isFunctionDeclaration(stmt) && returnsFunction(stmt)) p.push("closure");
  else if (ts.isVariableStatement(stmt)) {
    for (const decl of stmt.declarationList.declarations) {
      const init = decl.initializer;
      if (init && (ts.isArrowFunction(init) || ts.isFunctionExpression(init))) {
        if (init.typeParameters && init.typeParameters.length && !p.includes("generic_wrapper")) {
          p.push("generic_wrapper");
        }
        if (returnsFunction(init) && !p.includes("closure")) p.push("closure");
      }
    }
  }
  return p;
}

function namedFromImportClause(clause) {
  const syms = [];
  if (!clause) return syms;
  if (clause.name) syms.push(clause.name.text); // default import binding
  const nb = clause.namedBindings;
  if (nb) {
    if (ts.isNamespaceImport(nb)) syms.push("* as " + nb.name.text);
    // For `import { A as B }`, el.propertyName is the ORIGINAL exported name (A)
    // and el.name is the local alias (B). Record A — it is what matches the
    // target's exports, so an aliased import still counts that export as used.
    else if (ts.isNamedImports(nb))
      for (const el of nb.elements) syms.push((el.propertyName ?? el.name).text);
  }
  return syms;
}

// namedFromExportClause extracts the symbols a re-export pulls forward, so they
// count as "used" through barrel files: `export { X, Y } from './z'` -> [X, Y];
// `export * from './z'` -> ["*"]; `export * as NS from './z'` -> ["* as NS"].
function namedFromExportClause(node) {
  const ec = node.exportClause;
  if (!ec) return ["*"];
  if (ts.isNamespaceExport(ec)) return ["* as " + ec.name.text];
  if (ts.isNamedExports(ec)) return ec.elements.map((el) => el.name.text);
  return [];
}

function analyze(relPath, source) {
  const sf = ts.createSourceFile(
    relPath,
    source,
    ts.ScriptTarget.Latest,
    /* setParentNodes */ true,
    scriptKind(relPath),
  );

  const imports = [];
  const exports = [];
  const endpoints = [];
  const declarations = []; // top-level decls (exported or not) with line spans, for chunking

  function addImport(specifier, symbols, kind, node) {
    if (!specifier) return;
    imports.push({ specifier, symbols: symbols || [], kind, line: lineOf(sf, node) });
  }

  // Recursive walk: imports (anywhere), dynamic import()/require(), and
  // router-style endpoint calls (router.get('/x', ...)).
  function visit(node) {
    if (
      ts.isImportDeclaration(node) &&
      node.moduleSpecifier &&
      ts.isStringLiteral(node.moduleSpecifier)
    ) {
      addImport(
        node.moduleSpecifier.text,
        namedFromImportClause(node.importClause),
        "import",
        node,
      );
    } else if (
      ts.isExportDeclaration(node) &&
      node.moduleSpecifier &&
      ts.isStringLiteral(node.moduleSpecifier)
    ) {
      addImport(node.moduleSpecifier.text, namedFromExportClause(node), "reexport", node);
    } else if (ts.isCallExpression(node)) {
      // Dynamic import('x')
      if (
        node.expression.kind === ts.SyntaxKind.ImportKeyword &&
        node.arguments.length &&
        ts.isStringLiteralLike(node.arguments[0])
      ) {
        addImport(node.arguments[0].text, [], "dynamic", node);
      }
      // require('x')
      else if (
        ts.isIdentifier(node.expression) &&
        node.expression.text === "require" &&
        node.arguments.length &&
        ts.isStringLiteralLike(node.arguments[0])
      ) {
        addImport(node.arguments[0].text, [], "require", node);
      }
      // Express/Fastify: app.get('/path', handler) / router.post('/x', ...)
      else if (
        ts.isPropertyAccessExpression(node.expression) &&
        ts.isIdentifier(node.expression.name) &&
        ROUTER_METHODS.has(node.expression.name.text) &&
        node.arguments.length &&
        ts.isStringLiteralLike(node.arguments[0]) &&
        node.arguments[0].text.startsWith("/") // avoid map.get('key') false positives
      ) {
        const obj = node.expression.expression.getText(sf);
        // Best-effort handler: last argument's identifier/member text.
        const lastArg = node.arguments[node.arguments.length - 1];
        let handler = obj;
        if (lastArg && (ts.isIdentifier(lastArg) || ts.isPropertyAccessExpression(lastArg))) {
          handler = lastArg.getText(sf);
        }
        endpoints.push({
          method: node.expression.name.text.toUpperCase(),
          path: node.arguments[0].text,
          handler,
          source: "router",
          line: lineOf(sf, node),
        });
      }
    }
    ts.forEachChild(node, visit);
  }
  visit(sf);

  // Top-level exports + Next.js App Router HTTP handlers.
  for (const stmt of sf.statements) {
    const { exported, isDefault } = modifierFlags(stmt);

    if (!exported) {
      // `export { a, b }` (local re-export without a module specifier)
      if (
        ts.isExportDeclaration(stmt) &&
        !stmt.moduleSpecifier &&
        stmt.exportClause &&
        ts.isNamedExports(stmt.exportClause)
      ) {
        for (const el of stmt.exportClause.elements) {
          exports.push({ name: el.name.text, kind: "named", isDefault: false, line: lineOf(sf, stmt) });
        }
      } else if (ts.isExportAssignment(stmt)) {
        // `export default <expr>`
        exports.push({
          name: stmt.expression.getText(sf).slice(0, 60),
          kind: "default",
          isDefault: true,
          line: lineOf(sf, stmt),
        });
      }
      continue;
    }

    const patterns = detectPatterns(stmt);

    if (ts.isFunctionDeclaration(stmt)) {
      const name = stmt.name ? stmt.name.text : "default";
      exports.push({ name, kind: "function", isDefault, line: lineOf(sf, stmt), patterns });
      // Next.js App Router: exported function named after an HTTP verb.
      if (stmt.name && HTTP_METHODS.has(name)) {
        endpoints.push({ method: name, path: "", handler: name, source: "next-app-router", line: lineOf(sf, stmt) });
      }
    } else if (ts.isClassDeclaration(stmt)) {
      exports.push({ name: stmt.name ? stmt.name.text : "default", kind: "class", isDefault, line: lineOf(sf, stmt), patterns });
    } else if (ts.isInterfaceDeclaration(stmt)) {
      exports.push({ name: stmt.name.text, kind: "interface", isDefault: false, line: lineOf(sf, stmt), patterns });
    } else if (ts.isTypeAliasDeclaration(stmt)) {
      exports.push({ name: stmt.name.text, kind: "type", isDefault: false, line: lineOf(sf, stmt), patterns });
    } else if (ts.isEnumDeclaration(stmt)) {
      exports.push({ name: stmt.name.text, kind: "enum", isDefault: false, line: lineOf(sf, stmt), patterns });
    } else if (ts.isVariableStatement(stmt)) {
      for (const decl of stmt.declarationList.declarations) {
        if (ts.isIdentifier(decl.name)) {
          exports.push({ name: decl.name.text, kind: "variable", isDefault: false, line: lineOf(sf, stmt), patterns });
        }
      }
    }
  }

  // Structural declarations (every top-level decl, exported or not) with line
  // spans — the chunker slices the file by these boundaries.
  for (const stmt of sf.statements) {
    const { exported } = modifierFlags(stmt);
    let name = null;
    let kind = null;
    if (ts.isFunctionDeclaration(stmt) && stmt.name) {
      name = stmt.name.text;
      kind = "function";
    } else if (ts.isClassDeclaration(stmt) && stmt.name) {
      name = stmt.name.text;
      kind = "class";
    } else if (ts.isInterfaceDeclaration(stmt)) {
      name = stmt.name.text;
      kind = "interface";
    } else if (ts.isTypeAliasDeclaration(stmt)) {
      name = stmt.name.text;
      kind = "type";
    } else if (ts.isEnumDeclaration(stmt)) {
      name = stmt.name.text;
      kind = "enum";
    } else if (ts.isVariableStatement(stmt)) {
      const d = stmt.declarationList.declarations[0];
      if (d && ts.isIdentifier(d.name)) {
        name = d.name.text;
        kind = "variable";
      }
    }
    if (name) {
      declarations.push({
        name,
        kind,
        startLine: lineOf(sf, stmt),
        endLine: endLineOf(sf, stmt),
        exported: !!exported,
      });
    }
  }

  // --- Intra-file call graph: top-level decl -> top-level decl it invokes -----
  const callableNames = new Set();
  for (const d of declarations) callableNames.add(d.name);

  const calls = [];
  const callSeen = new Set();
  const addCall = (caller, callee) => {
    if (!callee || callee === caller || !callableNames.has(callee)) return;
    const key = caller + ">" + callee;
    if (callSeen.has(key)) return;
    callSeen.add(key);
    calls.push({ caller, callee });
  };

  const topLevelName = (stmt) => {
    if (ts.isFunctionDeclaration(stmt) && stmt.name) return stmt.name.text;
    if (ts.isClassDeclaration(stmt) && stmt.name) return stmt.name.text;
    if (ts.isVariableStatement(stmt)) {
      const d = stmt.declarationList.declarations[0];
      if (d && ts.isIdentifier(d.name)) return d.name.text;
    }
    return null;
  };
  const bodyOf = (stmt) => {
    if (ts.isFunctionDeclaration(stmt)) return stmt.body;
    if (ts.isClassDeclaration(stmt)) return stmt; // walk methods + their bodies
    if (ts.isVariableStatement(stmt)) {
      const d = stmt.declarationList.declarations[0];
      const init = d && d.initializer;
      if (init && (ts.isArrowFunction(init) || ts.isFunctionExpression(init))) return init.body;
    }
    return null;
  };

  const collectCalls = (caller, node) => {
    const walk = (n) => {
      if (!n) return;
      if (ts.isCallExpression(n) && ts.isIdentifier(n.expression)) {
        addCall(caller, n.expression.text); // direct call foo()
      } else if (ts.isNewExpression(n) && n.expression && ts.isIdentifier(n.expression)) {
        addCall(caller, n.expression.text); // construction new Foo()
      }
      ts.forEachChild(n, walk);
    };
    walk(node);
  };

  for (const stmt of sf.statements) {
    const caller = topLevelName(stmt);
    const body = bodyOf(stmt);
    if (caller && body) collectCalls(caller, body);
  }

  return { relPath, imports, exports, endpoints, declarations, calls };
}

async function main() {
  const raw = (await readStdin()).replace(/^﻿/, ""); // strip BOM if present
  let payload;
  try {
    payload = JSON.parse(raw);
  } catch (e) {
    process.stdout.write(JSON.stringify({ results: [], error: "invalid json input: " + e.message }));
    return;
  }

  const files = Array.isArray(payload.files) ? payload.files : [];
  const results = [];
  for (const f of files) {
    try {
      results.push(analyze(f.relPath, typeof f.source === "string" ? f.source : ""));
    } catch (e) {
      results.push({
        relPath: f.relPath,
        imports: [],
        exports: [],
        endpoints: [],
        error: String((e && e.message) || e),
      });
    }
  }
  process.stdout.write(JSON.stringify({ results }));
}

main().catch((e) => {
  process.stdout.write(JSON.stringify({ results: [], error: String((e && e.message) || e) }));
  process.exit(0);
});

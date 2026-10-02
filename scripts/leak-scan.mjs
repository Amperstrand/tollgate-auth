#!/usr/bin/env node
/**
 * Leak gate — publication scanner (zero deps by design).
 *
 * Exit 0 = clean, 1 = findings, 2 = usage error.
 * Every rule maps to the publication policy in README ("Publication & leak
 * policy"): no card numbers, no license plates, no phones, no tokens, no
 * captured payloads. Suppressions live in scripts/leak-scan-allowlist.txt
 * and REQUIRE an inline justification (# because: ...).
 *
 * Usage:
 *   node scripts/leak-scan.mjs [path] [--history]
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import { execSync } from "node:child_process";
import { join, relative } from "node:path";

const SKIP_DIRS = new Set([
  ".git",
  "node_modules",
  "dist",
  ".wrangler",
  ".omo",
  "coverage",
  ".playwright-mcp",
]);
const TEXT_EXT =
  /\.(ts|tsx|js|mjs|cjs|json|jsonc|md|txt|html|css|ya?ml|toml|sh|py|rs|go|env|example)$/;

function luhnValid(raw) {
  const digits = raw.replace(/[^\d]/g, "");
  if (digits.length < 13 || digits.length > 19) return false;
  if (/^0+$/.test(digits)) return false; // placeholder, not a PAN
  // 14-digit yyyymmddhhmmss timestamps (Go pseudo-versions, file stamps),
  // possibly preceded by a captured version-string digit ("0-2026…").
  const ts = digits.length === 15 && digits.startsWith("0")
    ? digits.slice(1)
    : digits.length === 14
      ? digits
      : "";
  if (ts.startsWith("20")) {
    const mm = Number(ts.slice(4, 6));
    const dd = Number(ts.slice(6, 8));
    if (mm >= 1 && mm <= 12 && dd >= 1 && dd <= 31) return false;
  }
  // Epoch-ms heuristic: 13-digit runs in the unix-millisecond range are
  // timestamps, not PANs (Luhn passes on ~10% of them by chance).
  if (digits.length === 13) {
    const asNumber = Number(digits);
    if (asNumber > 9e11 && asNumber < 4e12) return false;
  }
  let sum = 0;
  let dbl = false;
  for (let i = digits.length - 1; i >= 0; i -= 1) {
    let d = Number(digits[i]);
    if (dbl) {
      d *= 2;
      if (d > 9) d -= 9;
    }
    sum += d;
    dbl = !dbl;
  }
  return sum % 10 === 0;
}

const PLACEHOLDER =
  /^(x{3,}|\*{3,}|<[^>]+>|\$\{[^}]+\}|changeme|change-me.*|redacted.*|placeholder.*|example.*|your[-_].*|0+|(\d)\2+)$/i;

const UUID_LIKE =
  /\b[0-9a-fA-F]{4,}-[0-9a-fA-F]{4,}-[0-9a-fA-F]{4,}(?:-[0-9a-fA-F]{4,})?\b/g;
const ENV_REFERENCE = /^[A-Z][A-Z0-9_]*$/;

function isSecretishValue(raw) {
  const value = raw.trim();
  if (PLACEHOLDER.test(value)) return false;
  if (ENV_REFERENCE.test(value)) return false;
  if (/^(true|false|off|on|none|null)$/i.test(value)) return false;
  return true;
}

/** Token-level exceptions: strings that pattern-match a rule but are known
 *  technical identifiers, not personal data (ED25519 = ED + 25519 vs the
 *  Norwegian plate pattern). Keep this list short and justified. */
/** Token-level exceptions: pattern-matching strings that are known
 *  identifiers or this ecosystem's RESERVED FAKE values (convention since
 *  2026-09-30). ED25519 collides with the NO-plate pattern; the rest are
 *  canonical test values so allowlists stay for real data only. */
const KNOWN_TOKENS = new Set([
  "ED25519",
  "+4790000000",
  "90000000",
  "AB12345",
  "CD67890",
]);

const RULES = [
  {
    id: "pan",
    why: "credit card number (Luhn-valid)",
    regex: /\b(?:\d[ -]?){13,19}\b/g,
    prepare: UUID_LIKE,
    accept: (match) => luhnValid(match),
  },
  {
    id: "no-plate",
    why: "Norwegian license plate",
    regex: /\b[A-HJ-PR-Y]{2}\s?\d{5}\b/g,
  },
  {
    id: "de-plate",
    why: "German license plate",
    regex: /\b[A-ZÄÖÜ]{1,3}-[A-Z]{1,2}-\d{1,4}\b/g,
  },
  {
    id: "phone",
    why: "phone number with country code",
    regex: /\+\d{2}\s?\d{6,12}\b/g,
  },
  {
    id: "no-mobile",
    why: "Norwegian mobile number (bare 8-digit 4xx/9xx)",
    regex: /\b(?:4[0-9]|9[0-9])\d{6}\b/g,
    accept: (match) => !/^(\d)\1{7}$/.test(match),
  },
  { id: "jwt", why: "JWT", regex: /\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{4,}/g },
  {
    id: "cashu-token",
    why: "Cashu token",
    regex: /\bcashu[AB][A-Za-z0-9_-]{30,}/g,
  },
  {
    id: "gh-token",
    why: "GitHub token",
    regex: /\b(?:ghp|gho|ghu|ghs|github_pat)_[A-Za-z0-9_]{16,}/g,
  },
  {
    id: "secret-key",
    why: "secret-looking key/value pair",
    regex:
      /\b(pan|cvc|cvv|card_?number|license_?plate|plate|licen[cs]e_?number|phone_?number|password|api[_-]?key|secret|rune)\b["']?\s*[:=]\s*["'][^"']{4,}["']/gi,
  },
];

function loadAllowlist(root) {
  const path = join(root, "scripts", "leak-scan-allowlist.txt");
  const entries = [];
  let pendingReason = "";
  try {
    const lines = readFileSync(path, "utf8").split("\n");
    for (const line of lines) {
      if (line.startsWith("# because:")) {
        pendingReason = line.slice("# because:".length).trim();
        continue;
      }
      if (line.startsWith("#") || line.trim() === "") {
        pendingReason = "";
        continue;
      }
      const [scope] = line.split("#");
      const trimmed = scope.trim();
      if (pendingReason === "") {
        entries.push({ bad: `allowlist entry without justification: ${trimmed}` });
        continue;
      }
      const [rule, pathPrefix] = trimmed.split(":");
      entries.push({ rule, pathPrefix, reason: pendingReason });
      pendingReason = "";
    }
  } catch {
    // no allowlist file — nothing suppressed
  }
  return entries;
}

function allowed(allowlist, ruleId, filePath) {
  return allowlist.some(
    (entry) =>
      entry.rule === ruleId && filePath.startsWith(entry.pathPrefix ?? "\0"),
  );
}

function* walk(dir) {
  for (const entry of readdirSync(dir)) {
    if (SKIP_DIRS.has(entry)) continue;
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      yield* walk(full);
    } else if (TEXT_EXT.test(entry)) {
      yield full;
    }
  }
}

const SECRET_KEY_REGEX =
  /\b(pan|cvc|cvv|card_?number|license_?plate|plate|licen[cs]e_?number|phone_?number|password|api[_-]?key|secret|rune)\b["']?\s*[:=]\s*["']([^"']{4,})["']/i;

function secretValueOf(line) {
  return SECRET_KEY_REGEX.exec(line)?.[2] ?? null;
}

function* scanLine(line) {
  for (const rule of RULES) {
    const prepared =
      rule.prepare === undefined ? line : line.replace(rule.prepare, " ");
    rule.regex.lastIndex = 0;
    const matches = prepared.match(rule.regex);
    if (matches === null) continue;
    for (const match of matches) {
      if (KNOWN_TOKENS.has(match)) continue;
      if (rule.id === "secret-key") {
        const value = secretValueOf(prepared);
        if (value === null || !isSecretishValue(value)) continue;
      } else if (rule.accept !== undefined && !rule.accept(match)) {
        continue;
      }
      yield { rule: rule.id, why: rule.why, sample: match.slice(0, 24) };
    }
  }
}

function scanText(allowlist, findings, text, filePath, lineOffset = 0) {
  const lines = text.split("\n");
  for (const [i, line] of lines.entries()) {
    for (const hit of scanLine(line)) {
      if (allowed(allowlist, hit.rule, filePath)) continue;
      findings.push({
        ...hit,
        file: filePath,
        line: lineOffset + i + 1,
      });
    }
  }
}

/**
 * The gate guards what can be COMMITTED: git-visible files only (tracked
 * + untracked-not-ignored). Gitignored local-only dirs (e.g. restored
 * capture archives read by bundle generators) stay on disk by design and
 * must not permanently redden the gate — the moment one is `git add`ed it
 * becomes tracked and is scanned.
 */
function gitVisibleFiles(root) {
  const out = execSync(
    "git ls-files -z --cached --others --exclude-standard",
    { cwd: root, maxBuffer: 64 * 1024 * 1024 },
  );
  return out.toString().split("\0").filter((f) => f !== "" && TEXT_EXT.test(f));
}

function scanTree(root, allowlist, findings) {
  let files;
  try {
    files = gitVisibleFiles(root);
  } catch {
    console.error("leak-scan: not a git repo — falling back to full fs walk");
    files = [...walk(root)].map((f) => relative(root, f));
  }
  for (const rel of files) {
    scanText(allowlist, findings, readFileSync(join(root, rel), "utf8"), rel);
  }
}

function scanHistory(root, allowlist, findings) {
  const diff = execSync("git log -p --no-color --unified=0", {
    cwd: root,
    maxBuffer: 256 * 1024 * 1024,
  }).toString();
  let file = "";
  for (const line of diff.split("\n")) {
    if (line.startsWith("+++ b/")) {
      file = line.slice(6);
    } else if (line.startsWith("+") && !line.startsWith("+++")) {
      scanText(allowlist, findings, line.slice(1), file);
    }
  }
}

const args = process.argv.slice(2);
const history = args.includes("--history");
const root = args.find((a) => !a.startsWith("--")) ?? ".";
const findings = [];
const allowlist = loadAllowlist(root);
const allowlistErrors = allowlist.filter((entry) => entry.bad !== undefined);
for (const error of allowlistErrors) findings.push({ rule: "allowlist", why: error.bad, file: "scripts/leak-scan-allowlist.txt", line: 0, sample: "" });

scanTree(root, allowlist, findings);
if (history) scanHistory(root, allowlist, findings);

const seen = new Set();
const unique = findings.filter((f) => {
  const key = `${f.rule}:${f.file}:${f.line}:${f.sample}`;
  if (seen.has(key)) return false;
  seen.add(key);
  return true;
});

for (const f of unique) {
  console.error(`LEAK ${f.rule} (${f.why}) ${f.file}:${f.line} "${f.sample}"`);
}
console.error(
  `leak-scan: ${unique.length} finding(s) [root=${root}${history ? " +history" : ""}]`,
);
process.exit(unique.length > 0 ? 1 : 0);

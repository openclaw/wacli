#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";

import { css, faviconSvg, js, socialCardSvg, themeBootstrapScript, themeToggleMarkup } from "./docs-site-assets.mjs";
import { escapeAttr, escapeHtml, markdownToHtml, tocFromHtml, validateLinks } from "./docs-site-render.mjs";

const root = process.cwd();
const docsDir = path.join(root, "docs");
const outDir = path.join(root, "dist", "docs-site");
const repoBase = "https://github.com/openclaw/wacli";
const repoEditBase = `${repoBase}/edit/main/docs`;
const cname = readCname();
const siteBase = cname ? `https://${cname}` : "";

const productName = "wacli";
const productTagline = "WhatsApp in your terminal";
const productDescription =
  "A single Go CLI that pairs as a linked WhatsApp Web device, mirrors message history into local SQLite with FTS5 search, and exposes send, media, contact, and group workflows for terminals, scripts, and coding agents.";
const brewInstall = "brew install steipete/tap/wacli";

const sections = [
  ["Start", ["index.md", "install.md", "quickstart.md", "overview.md"]],
  ["Auth & Sync", ["auth.md", "accounts.md", "sync.md", "history.md", "doctor.md"]],
  ["Messages", ["messages.md", "send.md", "media.md", "presence.md", "channels.md"]],
  ["Contacts & Groups", ["contacts.md", "contacts-import-system.md", "chats.md", "groups.md", "profile.md"]],
  ["Reference", ["spec.md", "docs.md", "store.md", "integrations.md", "completion.md", "version.md", "help.md", "release.md"]],
];

const buildExcludes = [];

fs.rmSync(outDir, { recursive: true, force: true });
fs.mkdirSync(outDir, { recursive: true });

const allPages = allMarkdown(docsDir).map((file) => {
  const rel = path.relative(docsDir, file).replaceAll(path.sep, "/");
  const raw = fs.readFileSync(file, "utf8");
  const { frontmatter, body } = parseFrontmatter(raw);
  const cleaned = stripStrayDirectives(body);
  const title = frontmatter.title || firstHeading(cleaned) || titleize(path.basename(rel, ".md"));
  return { file, rel, title, outRel: outPath(rel, frontmatter), markdown: cleaned, frontmatter };
});

const pages = allPages.filter((page) => !buildExcludes.some((re) => re.test(page.rel)));
const pageMap = new Map(pages.map((page) => [page.rel, page]));
const permalinkMap = new Map();
for (const page of pages) {
  if (page.frontmatter.permalink) {
    permalinkMap.set(normalizePermalink(page.frontmatter.permalink), page);
  }
}

const nav = sections
  .map(([name, rels]) => ({
    name,
    pages: rels.map((rel) => pageMap.get(rel)).filter(Boolean),
  }))
  .filter((section) => section.pages.length);

const sectionByRel = new Map();
for (const section of nav) for (const page of section.pages) sectionByRel.set(page.rel, section.name);
const orderedPages = nav.flatMap((s) => s.pages);

for (const page of pages) {
  const html = markdownToHtml(page.markdown, page.rel, rewriteHref);
  const toc = tocFromHtml(html);
  const idx = orderedPages.findIndex((p) => p.rel === page.rel);
  const prev = idx > 0 ? orderedPages[idx - 1] : null;
  const next = idx >= 0 && idx < orderedPages.length - 1 ? orderedPages[idx + 1] : null;
  const sectionName = sectionByRel.get(page.rel) || "Reference";
  const pageOut = path.join(outDir, page.outRel);
  fs.mkdirSync(path.dirname(pageOut), { recursive: true });
  fs.writeFileSync(pageOut, layout({ page, html, toc, prev, next, sectionName }), "utf8");
}

fs.writeFileSync(path.join(outDir, "favicon.svg"), faviconSvg(), "utf8");
fs.writeFileSync(path.join(outDir, "social-card.svg"), socialCardSvg(), "utf8");
fs.writeFileSync(path.join(outDir, ".nojekyll"), "", "utf8");
if (cname) fs.writeFileSync(path.join(outDir, "CNAME"), cname, "utf8");
validateLinks(outDir);
fs.writeFileSync(path.join(outDir, "llms.txt"), llmsTxt(), "utf8");
console.log(`built docs site: ${path.relative(root, outDir)}`);

function llmsTxt() {
  const origin = docsOrigin();
  const source = docsSourceUrl();
  const name = typeof productName !== "undefined" ? productName : path.basename(root);
  const description = typeof productDescription !== "undefined" ? productDescription : `${name} documentation index.`;
  const install = docsInstallHint();
  const docPages = docsLlmsPages().map((page) => `- ${page.title}: ${pageUrl(origin, page.outRel)}`);
  const lines = [
    `# ${name}`,
    "",
    description,
    "",
    "Canonical documentation:",
    ...docPages,
  ];
  if (install) {
    lines.push("", "Install:", `- ${install}`);
  }
  if (source) {
    lines.push("", `Source: ${source}`);
  }
  lines.push("", "Guidance for agents:", "- Prefer the canonical documentation URLs above over README excerpts or package metadata.", "- Fetch only the pages needed for the current task; this is an index, not a full-site corpus.");
  return `${lines.join("\n")}\n`;
}

function docsLlmsPages() {
  const seen = new Set();
  const ordered = typeof orderedPages !== "undefined" ? orderedPages : [];
  return [...ordered, ...pages].filter((page) => page.outRel && !seen.has(page.outRel) && seen.add(page.outRel));
}

function docsOrigin() {
  const value =
    (typeof siteBase !== "undefined" && siteBase) ||
    (typeof siteUrl !== "undefined" && siteUrl) ||
    (typeof customDomain !== "undefined" && customDomain ? `https://${customDomain}` : "");
  return value.replace(/\/$/, "");
}

function docsSourceUrl() {
  if (typeof repoBase !== "undefined") return repoBase;
  if (typeof repoUrl !== "undefined") return repoUrl;
  if (typeof repoEditBase !== "undefined") return repoEditBase.replace(/\/edit\/main\/docs\/?$/, "");
  return "";
}

function docsInstallHint() {
  if (typeof installCommand !== "undefined") return installCommand;
  if (typeof installLine !== "undefined") return installLine;
  if (typeof installCmd !== "undefined") return installCmd;
  if (typeof installSnippet !== "undefined") return installSnippet;
  if (typeof brewInstall !== "undefined") return brewInstall;
  return "";
}

function pageUrl(origin, outRel) {
  const normalized = outRel === "index.html" ? "" : outRel.replace(/(?:^|\/)index\.html$/, (match) => match === "index.html" ? "" : "/");
  if (!origin) return normalized || "index.html";
  return normalized ? `${origin}/${normalized}` : `${origin}/`;
}

function readCname() {
  for (const candidate of [path.join(docsDir, "CNAME"), path.join(root, "CNAME")]) {
    if (fs.existsSync(candidate)) return fs.readFileSync(candidate, "utf8").trim();
  }
  return "";
}

function parseFrontmatter(raw) {
  const match = raw.match(/^---\n([\s\S]*?)\n---\n?/);
  if (!match) return { frontmatter: {}, body: raw };
  const fm = {};
  for (const line of match[1].split("\n")) {
    const m = line.match(/^([A-Za-z0-9_-]+):\s*(.*?)\s*$/);
    if (!m) continue;
    let value = m[2];
    if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
      value = value.slice(1, -1);
    }
    fm[m[1]] = value;
  }
  return { frontmatter: fm, body: raw.slice(match[0].length) };
}

function stripStrayDirectives(body) {
  return body
    .replace(/\r\n/g, "\n")
    .split("\n")
    .filter((line) => !/^\s*\{:\s*[^}]*\}\s*$/.test(line))
    .map((line) => line.replace(/\s*\{:\s*[^}]*\}\s*$/, ""))
    .join("\n");
}

function normalizePermalink(value) {
  let v = value.trim();
  if (!v) return "/";
  if (!v.startsWith("/")) v = `/${v}`;
  if (v.length > 1 && v.endsWith("/")) v = v.slice(0, -1);
  return v;
}

function allMarkdown(dir) {
  return fs
    .readdirSync(dir, { withFileTypes: true })
    .flatMap((entry) => {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) return allMarkdown(full);
      return entry.name.endsWith(".md") ? [full] : [];
    })
    .sort();
}

function outPath(rel, frontmatter = {}) {
  if (frontmatter.permalink) {
    const permalink = normalizePermalink(frontmatter.permalink);
    if (permalink === "/") return "index.html";
    return `${permalink.slice(1)}/index.html`;
  }
  if (rel === "index.md") return "index.html";
  if (rel === "README.md") return "index.html";
  if (rel.endsWith("/README.md")) return rel.replace(/README\.md$/, "index.html");
  return rel.replace(/\.md$/, ".html");
}

function firstHeading(markdown) {
  return markdown.match(/^#\s+(.+)$/m)?.[1]?.trim();
}

function titleize(input) {
  return input.replaceAll("-", " ").replace(/\b\w/g, (m) => m.toUpperCase());
}

function rewriteHref(href, currentRel) {
  if (/^(https?:|mailto:|tel:|#)/i.test(href)) return href;
  if (/^[a-z][a-z0-9+.-]*:/i.test(href)) {
    throw new Error(`unsupported docs link scheme in ${currentRel}: ${href}`);
  }
  const [raw, hash = ""] = href.split("#");
  if (!raw) return hash ? `#${hash}` : "";
  if (raw.startsWith("/")) {
    const target = permalinkMap.get(normalizePermalink(raw));
    if (target) {
      const currentOut = pageMap.get(currentRel)?.outRel || outPath(currentRel);
      const out = hrefToOutRel(target.outRel, currentOut);
      return hash ? `${out}#${hash}` : out;
    }
    return href;
  }
  if (!raw.endsWith(".md")) return href;
  const from = path.posix.dirname(currentRel);
  const target = path.posix.normalize(path.posix.join(from, raw));
  let rewritten = pageMap.get(target)?.outRel || outPath(target);
  const currentOut = pageMap.get(currentRel)?.outRel || outPath(currentRel);
  rewritten = hrefToOutRel(rewritten, currentOut);
  return `${rewritten}${hash ? `#${hash}` : ""}`;
}

function isHomePage(page) {
  if (page.frontmatter.permalink && normalizePermalink(page.frontmatter.permalink) === "/") return true;
  return page.rel === "index.md" || page.rel === "README.md";
}

function homeHero(page) {
  const description = page.frontmatter.description || productDescription;
  const installRel = pageMap.get("install.md")?.outRel
    ? hrefToOutRel(pageMap.get("install.md").outRel, page.outRel)
    : "install.html";
  const quickstartRel = pageMap.get("quickstart.md")?.outRel
    ? hrefToOutRel(pageMap.get("quickstart.md").outRel, page.outRel)
    : "quickstart.html";
  const features = ["Pair", "Sync", "Search", "Send", "Media", "Contacts", "Chats", "Groups", "History", "Presence", "Doctor"];
  return `<header class="home-hero">
        <p class="eyebrow">WhatsApp · One CLI</p>
        <h1>${escapeHtml(productTagline)}</h1>
        <p class="lede">${escapeHtml(description)}</p>
        <div class="home-cta">
          <a class="btn btn-primary" href="${quickstartRel}">Quickstart</a>
          <a class="btn btn-ghost" href="${repoBase}" rel="noopener">GitHub</a>
          <div class="home-install" aria-label="Install with Homebrew">
            <span class="prompt" aria-hidden="true">$</span>
            <code>${escapeHtml(brewInstall)}</code>
          </div>
        </div>
        <div class="home-services" aria-label="Capabilities">
          ${features.map((s) => `<span>${escapeHtml(s)}</span>`).join("")}
        </div>
        <p class="muted"><a href="${installRel}">Other install options →</a></p>
      </header>`;
}

function standardHero(page, sectionName, editUrl) {
  return `<header class="hero">
        <div class="hero-text">
          <p class="eyebrow">${escapeHtml(sectionName)}</p>
          <h1>${escapeHtml(page.title)}</h1>
        </div>
        <div class="hero-meta">
          <a class="repo" href="${repoBase}" rel="noopener">GitHub</a>
          <a class="edit" href="${escapeAttr(editUrl)}" rel="noopener">Edit page</a>
        </div>
      </header>`;
}

function layout({ page, html, toc, prev, next, sectionName }) {
  const depth = page.outRel.split("/").length - 1;
  const rootPrefix = depth ? "../".repeat(depth) : "";
  const editUrl = `${repoEditBase}/${page.rel}`;
  const home = isHomePage(page);
  const prevNext = !home && (prev || next) ? pageNavHtml(prev, next, page.outRel) : "";
  const heroBlock = home ? homeHero(page) : standardHero(page, sectionName, editUrl);
  const articleClass = home ? "doc doc-home" : "doc";
  const tocBlock = home ? "" : toc;
  const titleSuffix = home ? `${productName} — ${productTagline}` : `${page.title} — ${productName}`;
  const description = page.frontmatter.description || (home ? productDescription : `${page.title} — ${productName} CLI documentation.`);
  const canonicalUrl = pageCanonicalUrl(page);
  const socialImage = siteBase ? `${siteBase}/social-card.svg` : `${rootPrefix}social-card.svg`;
  const socialMeta = [
    ["link", "rel", "canonical", "href", canonicalUrl],
    ["meta", "property", "og:type", "content", "website"],
    ["meta", "property", "og:site_name", "content", productName],
    ["meta", "property", "og:title", "content", titleSuffix],
    ["meta", "property", "og:description", "content", description],
    ["meta", "property", "og:url", "content", canonicalUrl],
    ["meta", "property", "og:image", "content", socialImage],
    ["meta", "property", "og:image:width", "content", "1200"],
    ["meta", "property", "og:image:height", "content", "630"],
    ["meta", "name", "twitter:card", "content", "summary_large_image"],
    ["meta", "name", "twitter:title", "content", titleSuffix],
    ["meta", "name", "twitter:description", "content", description],
    ["meta", "name", "twitter:image", "content", socialImage],
  ].map(tagHtml).join("\n  ");
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${escapeHtml(titleSuffix)}</title>
  <meta name="description" content="${escapeAttr(description)}">
  ${socialMeta}
  <link rel="icon" href="${rootPrefix}favicon.svg" type="image/svg+xml">
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
  <script>${themeBootstrapScript()}</script>
  <style>${css()}</style>
</head>
<body${home ? ' class="home"' : ""}>
  <button class="nav-toggle" type="button" aria-label="Toggle navigation" aria-expanded="false">
    <span aria-hidden="true"></span><span aria-hidden="true"></span><span aria-hidden="true"></span>
  </button>
  <div class="shell">
    <aside class="sidebar">
      <div class="sidebar-head">
        <a class="brand" href="${hrefToOutRel("index.html", page.outRel)}" aria-label="${productName} docs home">
          <span class="mark" aria-hidden="true">
            <svg viewBox="0 0 32 32" xmlns="http://www.w3.org/2000/svg" width="28" height="28" role="presentation"><path d="M16 3a13 13 0 0 0-11.18 19.6L3 29l6.6-1.74A13 13 0 1 0 16 3Zm7.6 18.43c-.32.9-1.86 1.7-2.6 1.78-.66.07-1.5.1-2.43-.15a13.5 13.5 0 0 1-2.2-.83c-3.88-1.68-6.4-5.6-6.6-5.85-.18-.26-1.55-2.06-1.55-3.93 0-1.86.98-2.78 1.32-3.16.34-.38.74-.47.99-.47.25 0 .5 0 .71.01.23.01.54-.09.84.64.32.79 1.07 2.66 1.16 2.86.1.2.16.43.03.69-.13.26-.2.42-.4.65-.2.23-.42.5-.6.68-.2.2-.41.4-.18.79.23.39 1.04 1.71 2.23 2.77 1.53 1.36 2.83 1.78 3.22 1.97.39.2.62.16.84-.1.23-.27.97-1.13 1.23-1.52.26-.39.52-.32.88-.2.36.13 2.27 1.07 2.66 1.27.39.2.65.3.74.46.1.17.1.96-.22 1.86Z" fill="#25d366"/></svg>
          </span>
          <span><strong>${escapeHtml(productName)}</strong><small>WhatsApp CLI docs</small></span>
        </a>
        ${themeToggleMarkup()}
      </div>
      <label class="search"><span>Search</span><input id="doc-search" type="search" placeholder="auth, sync, send"></label>
      <nav>${navHtml(page)}</nav>
    </aside>
    <main>
      ${heroBlock}
      <div class="doc-grid${home ? " doc-grid-home" : ""}">
        <article class="${articleClass}">${html}${prevNext}</article>
        ${tocBlock}
      </div>
    </main>
  </div>
  <script>${js()}</script>
</body>
</html>`;
}

function pageCanonicalUrl(page) {
  if (!siteBase) return page.outRel;
  if (page.outRel === "index.html") return `${siteBase}/`;
  const rel = page.outRel.endsWith("/index.html") ? page.outRel.slice(0, -"index.html".length) : page.outRel;
  return `${siteBase}/${rel}`;
}

function tagHtml([tag, k1, v1, k2, v2]) {
  return tag === "link" ? `<link ${k1}="${v1}" ${k2}="${escapeAttr(v2)}">` : `<meta ${k1}="${v1}" ${k2}="${escapeAttr(v2)}">`;
}

function pageNavHtml(prev, next, currentOutRel) {
  const cell = (page, dir) => {
    if (!page) return "";
    return `<a class="page-nav-${dir}" href="${hrefToOutRel(page.outRel, currentOutRel)}"><small>${dir === "prev" ? "Previous" : "Next"}</small><span>${escapeHtml(page.title)}</span></a>`;
  };
  return `<nav class="page-nav" aria-label="Pager">${cell(prev, "prev")}${cell(next, "next")}</nav>`;
}

function navHtml(currentPage) {
  return nav
    .map((section) => `<section><h2>${escapeHtml(section.name)}</h2>${section.pages.map((page) => {
      const href = hrefToOutRel(page.outRel, currentPage.outRel);
      const active = page.rel === currentPage.rel ? " active" : "";
      return `<a class="nav-link${active}" href="${href}">${escapeHtml(navTitle(page))}</a>`;
    }).join("")}</section>`)
    .join("");
}

function navTitle(page) {
  if (page.rel === "index.md") return "Overview";
  return page.title.replace(/^`wacli\s*/, "").replace(/`$/, "");
}

function hrefToOutRel(targetOutRel, currentOutRel) {
  const currentDir = path.posix.dirname(currentOutRel);
  if (targetOutRel.endsWith("/index.html")) {
    const targetDir = targetOutRel.slice(0, -"index.html".length);
    const rel = path.posix.relative(currentDir, targetDir || ".") || ".";
    return rel.endsWith("/") ? rel : `${rel}/`;
  }
  if (targetOutRel === "index.html") {
    const rel = path.posix.relative(currentDir, ".") || ".";
    return rel.endsWith("/") ? rel : `${rel}/`;
  }
  return path.posix.relative(currentDir, targetOutRel) || path.posix.basename(targetOutRel);
}

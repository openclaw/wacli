import fs from "node:fs";
import path from "node:path";

import { highlight } from "./docs-site-highlight.mjs";

export function renderMarkdown(markdown, currentRel, rewriteHref) {
  const state = { headings: [], slugs: new Map() };
  const html = renderMarkdownInto(markdown, currentRel, rewriteHref, state);
  return { html, headings: state.headings };
}

function renderMarkdownInto(markdown, currentRel, rewriteHref, state) {
  const lines = markdown.replace(/\r\n/g, "\n").split("\n");
  const html = [];
  let paragraph = [];
  let list = null;
  let fence = null;
  let blockquote = [];

  const flushParagraph = () => {
    if (!paragraph.length) return;
    html.push(`<p>${inline(paragraph.join(" "), currentRel, rewriteHref).html}</p>`);
    paragraph = [];
  };
  const closeList = () => {
    if (!list) return;
    html.push(`</${list}>`);
    list = null;
  };
  const flushBlockquote = () => {
    if (!blockquote.length) return;
    const inner = renderMarkdownInto(blockquote.join("\n"), currentRel, rewriteHref, state);
    html.push(`<blockquote>${inner}</blockquote>`);
    blockquote = [];
  };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const fenceMatch = line.match(/^```([\w+-]+)?\s*$/);
    if (fenceMatch) {
      flushParagraph();
      closeList();
      flushBlockquote();
      if (fence) {
        html.push(`<pre><code class="language-${escapeAttr(fence.lang)}">${highlight(fence.lang, fence.lines.join("\n"))}</code></pre>`);
        fence = null;
      } else {
        fence = { lang: fenceMatch[1] || "text", lines: [] };
      }
      continue;
    }
    if (fence) {
      fence.lines.push(line);
      continue;
    }
    if (/^>\s?/.test(line)) {
      flushParagraph();
      closeList();
      blockquote.push(line.replace(/^>\s?/, ""));
      continue;
    }
    flushBlockquote();
    if (!line.trim()) {
      flushParagraph();
      closeList();
      continue;
    }
    if (/^\s*---+\s*$/.test(line)) {
      flushParagraph();
      closeList();
      html.push("<hr>");
      continue;
    }
    const heading = line.match(/^(#{1,4})\s+(.+)$/);
    if (heading) {
      flushParagraph();
      closeList();
      const level = heading[1].length;
      const text = heading[2].trim();
      const id = uniqueSlug(state.slugs, text);
      const inner = inline(text, currentRel, rewriteHref);
      if (level === 2 || level === 3) {
        state.headings.push({ level, id, label: inner.text });
      }
      if (level === 1) {
        html.push(`<h1 id="${id}">${inner.html}</h1>`);
      } else {
        html.push(`<h${level} id="${id}"><a class="anchor" href="#${id}" aria-label="Anchor link">#</a>${inner.html}</h${level}>`);
      }
      continue;
    }
    if (line.trimStart().startsWith("|") && line.includes("|", line.indexOf("|") + 1) && isDivider(lines[i + 1] || "")) {
      flushParagraph();
      closeList();
      const header = splitRow(line);
      const aligns = splitRow(lines[i + 1]).map((cell) => {
        const left = cell.startsWith(":");
        const right = cell.endsWith(":");
        return right && left ? "center" : right ? "right" : left ? "left" : "";
      });
      i += 1;
      const rows = [];
      while (i + 1 < lines.length && lines[i + 1].trimStart().startsWith("|")) {
        i += 1;
        rows.push(splitRow(lines[i]));
      }
      const th = header.map((c, idx) => `<th${aligns[idx] ? ` style="text-align:${aligns[idx]}"` : ""}>${inline(c, currentRel, rewriteHref).html}</th>`).join("");
      const tb = rows.map((r) => `<tr>${r.map((c, idx) => `<td${aligns[idx] ? ` style="text-align:${aligns[idx]}"` : ""}>${inline(c, currentRel, rewriteHref).html}</td>`).join("")}</tr>`).join("");
      html.push(`<table><thead><tr>${th}</tr></thead><tbody>${tb}</tbody></table>`);
      continue;
    }
    const bullet = line.match(/^\s*-\s+(.+)$/);
    const numbered = line.match(/^\s*\d+\.\s+(.+)$/);
    if (bullet || numbered) {
      flushParagraph();
      const tag = bullet ? "ul" : "ol";
      if (list && list !== tag) closeList();
      if (!list) {
        list = tag;
        html.push(`<${tag}>`);
      }
      html.push(`<li>${inline((bullet || numbered)[1], currentRel, rewriteHref).html}</li>`);
      continue;
    }
    paragraph.push(line.trim());
  }
  flushParagraph();
  closeList();
  flushBlockquote();
  return html.join("\n");
}

export function tocFromHeadings(headings) {
  if (headings.length < 2) return "";
  return `<nav class="toc" aria-label="On this page"><h2>On this page</h2>${headings
    .map((heading) => `<a class="toc-l${heading.level}" href="#${heading.id}">${escapeHtml(heading.label)}</a>`)
    .join("")}</nav>`;
}

export function validateLinks(outputDir) {
  const failures = [];
  const placeholderHrefs = /^(url|path|file|dir|name)$/i;
  for (const file of allHtml(outputDir)) {
    const html = fs.readFileSync(file, "utf8");
    for (const match of html.matchAll(/href="([^"]+)"/g)) {
      const href = match[1];
      if (/^(#|https?:|mailto:|tel:)/i.test(href)) continue;
      if (/^[a-z][a-z0-9+.-]*:/i.test(href)) {
        failures.push(`${path.relative(outputDir, file)}: unsupported link scheme ${href}`);
        continue;
      }
      if (placeholderHrefs.test(href)) continue;
      const [rawPath, anchor = ""] = href.split("#");
      const targetPath = rawPath ? path.resolve(path.dirname(file), rawPath) : file;
      const target = fs.existsSync(targetPath) && fs.statSync(targetPath).isDirectory()
        ? path.join(targetPath, "index.html")
        : targetPath;
      if (!fs.existsSync(target)) {
        failures.push(`${path.relative(outputDir, file)}: ${href} -> missing ${path.relative(outputDir, target)}`);
        continue;
      }
      if (anchor) {
        const targetHtml = fs.readFileSync(target, "utf8");
        if (!targetHtml.includes(`id="${anchor}"`) && !targetHtml.includes(`name="${anchor}"`)) {
          failures.push(`${path.relative(outputDir, file)}: ${href} -> missing anchor`);
        }
      }
    }
  }
  if (failures.length) {
    throw new Error(`broken docs links:\n${failures.join("\n")}`);
  }
}

export function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char]);
}

export function escapeAttr(value) {
  return escapeHtml(value);
}

function inline(text, currentRel, rewriteHref) {
  const tokens = [];
  const stash = (token) => {
    tokens.push(token);
    return `\u0000${tokens.length - 1}\u0000`;
  };
  let value = text.replace(/`([^`]+)`/g, (_, code) => {
    return stash({ kind: "code", value: code });
  });
  value = tokenizeInline(escapeHtml(value));
  return renderInlineValue(value, tokens, false);

  function tokenizeInline(escaped) {
    return escaped
    .replace(/&lt;br&gt;/g, () => stash({ kind: "break" }))
    .replace(/\*\*([^*]+)\*\*/g, (_, inner) => stash({ kind: "strong", value: tokenizeInline(inner) }))
    .replace(/(^|[^*])\*([^*\s][^*]*?)\*(?!\*)/g, (_, prefix, inner) => `${prefix}${stash({ kind: "em", value: tokenizeInline(inner) })}`)
    .replace(/(^|[^_])_([^_\s][^_]*?)_(?!_)/g, (_, prefix, inner) => `${prefix}${stash({ kind: "em", value: tokenizeInline(inner) })}`)
    .replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_, label, href) => {
      return stash({ kind: "link", value: tokenizeInline(label), href: rewriteHref(href, currentRel) });
    })
    .replace(/&lt;(https?:\/\/[^\s<>]+)&gt;/g, (_, href) => {
      return stash({ kind: "autolink", href });
    })
    .replace(/\\\|/g, "|");
  }
}

function renderInlineValue(value, tokens, linkLabel) {
  const marker = /\u0000(\d+)\u0000/g;
  let html = "";
  let text = "";
  let offset = 0;
  for (const match of value.matchAll(marker)) {
    const escaped = value.slice(offset, match.index);
    html += escaped;
    text += decodeEscapedText(escaped);
    const rendered = renderInlineToken(tokens[Number(match[1])], tokens, linkLabel);
    html += rendered.html;
    text += rendered.text;
    offset = match.index + match[0].length;
  }
  const escaped = value.slice(offset);
  return {
    html: html + escaped,
    text: text + decodeEscapedText(escaped),
  };
}

function renderInlineToken(token, tokens, linkLabel) {
  if (token.kind === "code") {
    return { html: `<code>${escapeHtml(token.value)}</code>`, text: token.value };
  }
  if (token.kind === "break") {
    return linkLabel ? { html: "&lt;br&gt;", text: "<br>" } : { html: "<br>", text: "" };
  }
  if (token.kind === "autolink") {
    return {
      html: `<a href="${token.href}">${token.href}</a>`,
      text: decodeEscapedText(token.href),
    };
  }
  const inner = renderInlineValue(token.value, tokens, token.kind === "link" || linkLabel);
  if (token.kind === "strong") {
    return { html: `<strong>${inner.html}</strong>`, text: inner.text };
  }
  if (token.kind === "em") {
    return { html: `<em>${inner.html}</em>`, text: inner.text };
  }
  return {
    html: `<a href="${escapeAttr(token.href)}">${inner.html}</a>`,
    text: inner.text,
  };
}

function decodeEscapedText(value) {
  return value.replace(/&(amp|lt|gt|quot|#39);/g, (entity) => {
    return ({ "&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": '"', "&#39;": "'" })[entity];
  });
}

function splitRow(line) {
  let trimmed = line.trim();
  if (trimmed.startsWith("|")) trimmed = trimmed.slice(1);
  if (trimmed.endsWith("|") && !trimmed.endsWith("\\|")) trimmed = trimmed.slice(0, -1);
  const cells = [];
  let current = "";
  for (let idx = 0; idx < trimmed.length; idx++) {
    const char = trimmed[idx];
    if (char === "\\" && trimmed[idx + 1] === "|") {
      current += "\\|";
      idx += 1;
      continue;
    }
    if (char === "|") {
      cells.push(current.trim().replace(/\\\|/g, "|"));
      current = "";
      continue;
    }
    current += char;
  }
  cells.push(current.trim().replace(/\\\|/g, "|"));
  return cells;
}

function isDivider(line) {
  return /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)+\|?\s*$/.test(line);
}

function slug(text) {
  return text.toLowerCase().replace(/`/g, "").replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
}

function uniqueSlug(slugs, text) {
  const base = slug(text) || "section";
  const count = (slugs.get(base) || 0) + 1;
  slugs.set(base, count);
  return count === 1 ? base : `${base}-${count}`;
}

function allHtml(dir) {
  return fs
    .readdirSync(dir, { withFileTypes: true })
    .flatMap((entry) => {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) return allHtml(full);
      return entry.name.endsWith(".html") ? [full] : [];
    })
    .sort();
}
